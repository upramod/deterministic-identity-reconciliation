// Command converge previews or applies one persisted identity projection.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/upramod/deterministic-identity-reconciliation/pkg/adapter/scim"
	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
	"github.com/upramod/deterministic-identity-reconciliation/pkg/store/postgres"
)

type report struct {
	SubjectID  string `json:"subject_id"`
	RowVersion int64  `json:"row_version"`
	Mode       string `json:"mode"`
	Action     string `json:"action"`
}

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, out, diagnostic io.Writer) error {
	flags := flag.NewFlagSet("converge", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	subject := flags.String("subject", "", "subject ID in canonical_state")
	subjectAttribute := flags.String("subject-attribute", "externalId", "SCIM attribute that binds the persisted subject ID")
	apply := flags.Bool("apply", false, "apply target changes; default is read-only preview")
	managed := flags.String("managed-attributes", "", "comma-separated owned scalar SCIM attributes")
	mode := flags.String("deprovision-mode", "disable", "disable or delete")
	timeout := flags.Duration("timeout", 30*time.Second, "total operation deadline")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*subject) == "" || flags.NArg() != 0 {
		return errors.New("-subject is required; positional arguments are not supported")
	}
	if *timeout <= 0 {
		return errors.New("-timeout must be positive")
	}
	dsn := getenv("IDENTITY_DATABASE_URL")
	if dsn == "" {
		return errors.New("IDENTITY_DATABASE_URL is required")
	}
	attributes := []string{}
	allowed := map[string]bool{}
	if strings.TrimSpace(*managed) != "" {
		for _, attribute := range strings.Split(*managed, ",") {
			attribute = strings.TrimSpace(attribute)
			attributes = append(attributes, attribute)
			allowed[attribute] = true
		}
	}
	adapter, err := scim.New(scim.Config{BaseURL: getenv("IDENTITY_SCIM_URL"), Token: getenv("IDENTITY_SCIM_TOKEN"), SubjectAttribute: *subjectAttribute, ManagedAttributes: attributes, DeprovisionMode: scim.DeprovisionMode(*mode)})
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return errors.New("open configured PostgreSQL connection failed")
	}
	defer db.Close()
	store, err := postgres.New(db)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result := report{SubjectID: *subject, Mode: "preview"}
	process := func(desired reconcile.IdentityProjection) error {
		result.RowVersion = desired.RowVersion
		for key := range desired.Attributes {
			if !allowed[key] {
				return fmt.Errorf("projection attribute %q is not in -managed-attributes", key)
			}
		}
		if *apply {
			result.Mode = "apply"
			converged, err := reconcile.Converge(ctx, adapter, desired)
			if err != nil {
				return err
			}
			result.Action = string(converged.Action)
			return nil
		}
		observed, err := adapter.Observe(ctx, desired.SubjectID)
		if err != nil {
			return err
		}
		result.Action = "would_update"
		if adapter.StateEquivalent(observed, desired) {
			result.Action = "unchanged"
		}
		return nil
	}
	if *apply {
		err = store.WithLockedProjection(ctx, *subject, process)
	} else {
		var desired reconcile.IdentityProjection
		desired, err = store.LoadProjection(ctx, *subject)
		if err == nil {
			err = process(desired)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("subject has no persisted projection; no target action taken")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
