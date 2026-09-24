package main

import (
	"context"
	"fmt"
	"os"

	"github.com/oisee/open-rfc-go/rfc"
	"github.com/spf13/cobra"

	"github.com/oisee/vibing-steampunk/pkg/saprfc"
)

var transportImportCmd = &cobra.Command{
	Use:   "import <REQUEST> [REQUEST ...]",
	Short: "Import released requests into this system, as STMS_IMPORT does there (classic RFC)",
	Long: `Import released requests into the system named by -s, in its client or
--client, through TMS -- CTS_API_IMPORT_CHANGE_REQUEST over classic RFC,
called in that system itself, which is where STMS_IMPORT runs too. From the
domain controller TMS would need the target's TMSSUP logon, which a remote
call cannot give, so the target is always the connected system.

The import changes the system, so it is off unless the system allows it:
allow_transport_import in .vsp.json or SAP_ALLOW_TRANSPORT_IMPORT=true,
independently of read_only.

  vsp -s qas transport import TR-A
  vsp -s qas transport import TR-A TR-B --client 200 --json`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params, err := resolveSystemParams(cmd)
		if err != nil {
			return err
		}
		if !params.AllowTransportImport {
			return fmt.Errorf("importing requests is off for this system; set allow_transport_import in .vsp.json or SAP_ALLOW_TRANSPORT_IMPORT=true")
		}
		client, _ := cmd.Flags().GetString("client")
		if client == "" {
			client = params.Client
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		return withRFC(cmd, func(ctx context.Context, c *rfc.Client) error {
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

func init() {
	transportImportCmd.Flags().String("client", "", "Target client (default: the system's client)")
	transportImportCmd.Flags().String("rfc-host", "", "RFC gateway host (default: rfc_host or the host of the system URL)")
	transportImportCmd.Flags().Bool("json", false, "Emit JSON")
	transportCmd.AddCommand(transportImportCmd)
}
