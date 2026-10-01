package adt

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Some repository operations have no remote-enabled module behind them, and
// the ones they use end an HTTP session or a WebSocket bridge the moment they
// touch anything GUI-shaped. For those vsp writes the sequence into a
// temporary report in $TMP, runs it as a background job and reads the outcome
// from the job log. This file is that report's life cycle.

// BackgroundRunner is what a temporary report needs beyond ADT: running a
// report as a background job and returning the texts of its job log, and
// reading a table. The MCP layer implements it over RFC (XBP, RFC_READ_TABLE).
type BackgroundRunner interface {
	RunReport(ctx context.Context, program string) ([]string, error)
	ReadTable(ctx context.Context, table, where string, fields []string, max int) ([]map[string]string, error)
}

// ClassicBadiRunner is the name the SXCI operations first gave BackgroundRunner.
type ClassicBadiRunner = BackgroundRunner

// ErrJobNotEnded is wrapped by a runner whose job was still running when its
// wait ran out. The report it runs is then left in place, as deleting it would
// not stop the job; the next run's sweep removes it.
var ErrJobNotEnded = errors.New("the background job has not ended")

// tempReportPrefixes are the names vsp gives its temporary reports. Only these,
// followed by the creation time, are ever swept.
var tempReportPrefixes = []string{"ZTEMP_SXCI_", "ZTEMP_SXCD_", "ZTEMP_SEGM_"}

// tempReportMaxAge is how old a temporary report must be before a later run
// takes it for one left behind. No vsp job waits nearly this long.
const tempReportMaxAge = time.Hour

// tempReportCleanupTimeout bounds deleting a report once its caller has gone.
const tempReportCleanupTimeout = time.Minute

// tempRun is what running a temporary report returned.
type tempRun struct {
	Lines    []string
	Program  string
	Warnings []string
}

// objectPackage returns the package of R3TR <object> <name>, and false if
// there is no such TADIR entry.
func objectPackage(ctx context.Context, run BackgroundRunner, object, name string) (string, bool, error) {
	where := fmt.Sprintf("PGMID = 'R3TR' AND OBJECT = '%s' AND OBJ_NAME = '%s'", object, strings.ReplaceAll(name, "'", "''"))
	rows, err := run.ReadTable(ctx, "TADIR", where, []string{"DEVCLASS"}, 1)
	if err != nil {
		return "", false, fmt.Errorf("reading TADIR: %w", err)
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return strings.TrimSpace(rows[0]["DEVCLASS"]), true, nil
}

// tempReportName is prefix plus the creation time in milliseconds, which is
// what lets a later run tell a report left behind from one still in use.
func tempReportName(prefix string, now time.Time) string {
	return fmt.Sprintf("%s%013d", prefix, now.UnixMilli())
}

// tempReportCreated reads the creation time back out of a temporary report's
// name, and false for any other name.
func tempReportCreated(name string) (time.Time, bool) {
	for _, p := range tempReportPrefixes {
		rest, ok := strings.CutPrefix(name, p)
		if !ok || len(rest) != 13 {
			continue
		}
		ms, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.UnixMilli(ms), true
	}
	return time.Time{}, false
}

// runTempReport writes a report into $TMP, activates it, runs it as a
// background job and deletes it again, returning the job log texts.
//
// The report is deleted whatever happened to the job, on a context of its own:
// a caller that gave up -- a cancelled MCP call, a closed client -- must not
// leave it behind. A report that could not be deleted is named in Warnings.
// Reports an earlier run left behind (a vsp that was killed half-way) are
// swept here too, once this run has shown that $TMP may be written.
func (c *Client) runTempReport(ctx context.Context, prefix string, source func(prog string) string, run BackgroundRunner) (*tempRun, error) {
	out := &tempRun{Program: tempReportName(prefix, time.Now())}
	prog := out.Program
	objectURL := tempReportURL(prog)

	if err := c.CreateObject(ctx, CreateObjectOptions{
		ObjectType: ObjectTypeProgram, Name: prog,
		Description: "vsp temporary report", PackageName: "$TMP",
	}); err != nil {
		return out, fmt.Errorf("creating the temporary report %s: %w", prog, err)
	}
	ctx = withMutationPackageChecked(ctx, objectURL)
	keep := false
	defer func() {
		if keep {
			out.Warnings = append(out.Warnings, fmt.Sprintf("the temporary report %s is kept while its job runs; a later run deletes it", prog))
			return
		}
		if err := c.deleteTempReport(ctx, prog); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("the temporary report %s could not be deleted (%v); a later run deletes it", prog, err))
		}
	}()
	out.Warnings = append(out.Warnings, c.sweepTempReports(ctx, run, prog)...)

	lock, err := c.LockObject(ctx, objectURL, "MODIFY")
	if err != nil {
		return out, fmt.Errorf("locking the temporary report %s: %w", prog, err)
	}
	if err := c.UpdateSource(ctx, objectURL+"/source/main", source(prog), lock.LockHandle, ""); err != nil {
		if uerr := c.releaseLockAfterFailure(ctx, objectURL, lock.LockHandle); uerr != nil {
			out.Warnings = append(out.Warnings, strandedLockAdvice(objectURL, uerr))
		}
		return out, fmt.Errorf("writing the temporary report %s: %w", prog, err)
	}
	if err := c.UnlockObject(ctx, objectURL, lock.LockHandle); err != nil {
		return out, fmt.Errorf("unlocking the temporary report %s: %w", prog, err)
	}
	activation, err := c.Activate(ctx, objectURL, prog)
	if err != nil {
		return out, fmt.Errorf("activating the temporary report %s: %w", prog, err)
	}
	if failure := compileFailure(activation, prog, 0); failure != nil {
		return out, fmt.Errorf("the generated report %s does not compile on this system: %s", prog, failure.Title)
	}
	lines, err := run.RunReport(ctx, prog)
	if err != nil {
		keep = errors.Is(err, ErrJobNotEnded)
		return out, err
	}
	out.Lines = lines
	return out, nil
}

func tempReportURL(prog string) string {
	return "/sap/bc/adt/programs/programs/" + url.PathEscape(strings.ToLower(prog))
}

// deleteTempReport deletes one temporary report on a context that outlives
// the caller's, marked as a $TMP object like the report runTempReport created.
func (c *Client) deleteTempReport(ctx context.Context, prog string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tempReportCleanupTimeout)
	defer cancel()
	objectURL := tempReportURL(prog)
	ctx = withMutationPackageChecked(ctx, objectURL)
	lock, err := c.LockObject(ctx, objectURL, "MODIFY")
	if err != nil {
		return err
	}
	if err := c.DeleteObject(ctx, objectURL, lock.LockHandle, ""); err != nil {
		if uerr := c.releaseLockAfterFailure(ctx, objectURL, lock.LockHandle); uerr != nil {
			return fmt.Errorf("%w; %s", err, strandedLockAdvice(objectURL, uerr))
		}
		return err
	}
	return nil
}

// sweepTempReports deletes the caller's temporary reports in $TMP that are
// older than tempReportMaxAge -- left behind by a run that never got to its
// own cleanup -- unless a job that has not ended still runs one of them. It is
// best effort: what cannot be read or deleted is named in the warnings it
// returns and left for the next run.
//
// The caller is whoever authored the report this run just created. The
// configured user would not do: cookie and SSO logons leave it empty.
func (c *Client) sweepTempReports(ctx context.Context, run BackgroundRunner, current string) []string {
	own, err := run.ReadTable(ctx, "TADIR", fmt.Sprintf("PGMID = 'R3TR' AND OBJECT = 'PROG' AND OBJ_NAME = '%s'", current), []string{"AUTHOR"}, 1)
	if err != nil || len(own) == 0 {
		return []string{fmt.Sprintf("looking for temporary reports left behind failed: cannot read the author of %s (%v)", current, err)}
	}
	user := strings.TrimSpace(own[0]["AUTHOR"])
	if user == "" {
		return nil
	}
	where := fmt.Sprintf("PGMID = 'R3TR' AND OBJECT = 'PROG' AND DEVCLASS = '$TMP' AND AUTHOR = '%s' AND OBJ_NAME LIKE 'ZTEMP_%%'",
		strings.ReplaceAll(user, "'", "''"))
	rows, err := run.ReadTable(ctx, "TADIR", where, []string{"OBJ_NAME"}, 0)
	if err != nil {
		return []string{fmt.Sprintf("looking for temporary reports left behind failed: %v", err)}
	}
	var warnings []string
	for _, row := range staleTempReports(rows, current, time.Now()) {
		busy, err := reportInUse(ctx, run, row)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("kept the temporary report %s: cannot tell whether a job still runs it (%v)", row, err))
			continue
		}
		if busy {
			continue
		}
		if err := c.deleteTempReport(ctx, row); err != nil {
			warnings = append(warnings, fmt.Sprintf("the temporary report %s, left behind by an earlier run, could not be deleted: %v", row, err))
			continue
		}
		warnings = append(warnings, fmt.Sprintf("deleted the temporary report %s, left behind by an earlier run", row))
	}
	return warnings
}

// reportInUse says whether a job that has not ended has a step running prog.
// A job can wait in the queue longer than tempReportMaxAge.
func reportInUse(ctx context.Context, run BackgroundRunner, prog string) (bool, error) {
	steps, err := run.ReadTable(ctx, "TBTCP", fmt.Sprintf("PROGNAME = '%s'", prog), []string{"JOBNAME", "JOBCOUNT"}, 0)
	if err != nil {
		return false, err
	}
	for _, st := range steps {
		where := fmt.Sprintf("JOBNAME = '%s' AND JOBCOUNT = '%s'",
			strings.ReplaceAll(strings.TrimSpace(st["JOBNAME"]), "'", "''"), strings.TrimSpace(st["JOBCOUNT"]))
		jobs, err := run.ReadTable(ctx, "TBTCO", where, []string{"STATUS"}, 1)
		if err != nil {
			return false, err
		}
		// P scheduled, S released, Y ready, R running: not ended.
		if len(jobs) == 0 {
			continue
		}
		if status := strings.TrimSpace(jobs[0]["STATUS"]); status != "" && strings.Contains("PSYR", status) {
			return true, nil
		}
	}
	return false, nil
}

// staleTempReports picks from TADIR rows the temporary reports older than
// tempReportMaxAge, never the current one.
func staleTempReports(rows []map[string]string, current string, now time.Time) []string {
	var stale []string
	for _, row := range rows {
		name := strings.TrimSpace(row["OBJ_NAME"])
		if name == current {
			continue
		}
		if created, ok := tempReportCreated(name); ok && now.Sub(created) > tempReportMaxAge {
			stale = append(stale, name)
		}
	}
	return stale
}
