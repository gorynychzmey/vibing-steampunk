package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/oisee/open-rfc-go/rfc"
	"github.com/spf13/cobra"

	"github.com/oisee/vibing-steampunk/pkg/adt"
	"github.com/oisee/vibing-steampunk/pkg/saprfc"
)

var transportImportCmd = &cobra.Command{
	Use:   "import <REQUEST> [REQUEST ...]",
	Short: "Import released requests into this system, as STMS_IMPORT does there (classic RFC)",
	Long: `Import released requests into the system named by -s, in its client,
through TMS -- CTS_API_IMPORT_CHANGE_REQUEST over classic RFC,
called in that system itself, which is where STMS_IMPORT runs too. From the
domain controller TMS would need the target's TMSSUP logon, which a remote
call cannot give, so the target is always the connected system.

The import changes the system, so it is off unless the system allows it:
allow_transport_import in .vsp.json or SAP_ALLOW_TRANSPORT_IMPORT=true. Even
then read_only and transport_read_only refuse it, and allowed_transports
limits which requests it takes.

  vsp -s qas transport import TR-A
  vsp -s qas transport import TR-A TR-B --json`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params, err := resolveSystemParams(cmd)
		if err != nil {
			return err
		}
		if err := checkImportAllowed(params, args); err != nil {
			return err
		}
		flagClient, _ := cmd.Flags().GetString("client")
		client, err := importClient(flagClient, params.Client)
		if err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		return withRFCTimeout(cmd, timeout, func(ctx context.Context, c *rfc.Client) error {
			res, ierr := saprfc.ImportRequests(ctx, c, args, client)
			if asJSON && res != nil {
				if perr := printJSON(res); perr != nil {
					return perr
				}
			} else if res != nil {
				for _, r := range res.Requests {
					fmt.Fprintf(os.Stderr, "%s -> %s client %s: retcode %s, %d tp step(s), worst rc %s\n",
						r.Request, res.System, res.Client, r.RetCode, len(r.Steps), r.MaxRC)
				}
				fmt.Fprintln(os.Stderr, res.Message)
			}
			return ierr
		})
	},
}

// checkImportAllowed applies the system's safety settings to an import: its
// own opt-in, then read-only, transport read-only and the allowed transports.
func checkImportAllowed(params *systemParams, requests []string) error {
	safety := adt.SafetyConfig{
		ReadOnly:             params.ReadOnly,
		TransportReadOnly:    params.TransportReadOnly,
		AllowedTransports:    params.AllowedTransports,
		AllowTransportImport: params.AllowTransportImport,
	}
	return safety.CheckTransportImport(requests)
}

// importClient is the client an import goes into: the system's own. The
// opt-in and the safety settings belong to the system entry, URL and client,
// so --client is taken only when it names that client.
func importClient(flag, own string) (string, error) {
	flag, own = strings.TrimSpace(flag), strings.TrimSpace(own)
	if flag == "" || flag == own {
		return own, nil
	}
	return "", fmt.Errorf("--client %s is blocked: it differs from the system's own client %q, and "+
		"allow_transport_import and the safety settings belong to that client (configure the other "+
		"client as its own system in .vsp.json and use -s with it)", flag, own)
}

func init() {
	transportImportCmd.Flags().String("client", "", "Target client; only the system's own client is accepted")
	transportImportCmd.Flags().String("rfc-host", "", "RFC gateway host (default: rfc_host or the host of the system URL)")
	transportImportCmd.Flags().Bool("json", false, "Emit JSON")
	transportImportCmd.Flags().Duration("timeout", saprfc.ImportTimeout, "How long to wait for the import (tp runs while the call waits)")
	transportCmd.AddCommand(transportImportCmd)
}
