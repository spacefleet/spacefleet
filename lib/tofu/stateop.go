package tofu

import (
	"errors"
	"fmt"
	"strings"
)

// State operations — the things people otherwise do from a laptop with
// production credentials, offered as a fixed menu of typed operations (never
// a free-form command line). Each is one gated, audited workflow run of a
// single execution unit for one OpenTofu component; the run's approval gate
// shows exactly the command that will run (see StateOp.Command).
const (
	// StateOpForceUnlock releases a stuck state lock: `tofu force-unlock`.
	// The lock id is the one OpenTofu prints in the "Error acquiring the
	// state lock" message of the run that could not proceed.
	StateOpForceUnlock = "force_unlock"
	// StateOpRemove forgets a resource: `tofu state rm`. The real
	// infrastructure is untouched; OpenTofu simply stops managing it.
	StateOpRemove = "rm"
	// StateOpMove renames a resource in state: `tofu state mv`, so a
	// refactor (a rename, a move into a module) is not a destroy + create.
	StateOpMove = "mv"
	// StateOpImport adopts existing infrastructure into state: `tofu import`.
	StateOpImport = "import"
)

// StateOp is one requested state operation with its typed fields — the
// per-run arguments stored on the workflow run (as JSON) and rendered into
// the step's script. Which fields apply depends on Operation (see Validate).
type StateOp struct {
	Operation string `json:"operation"`
	// Address is the resource address the operation targets (rm, mv, import).
	Address string `json:"address,omitempty"`
	// NewAddress is the destination address of a move.
	NewAddress string `json:"new_address,omitempty"`
	// LockID is the lock id a force-unlock releases.
	LockID string `json:"lock_id,omitempty"`
	// ImportID is the provider-specific identifier an import adopts.
	ImportID string `json:"import_id,omitempty"`
}

// ErrInvalidStateOp is the sentinel every Validate failure wraps, so a
// handler can map any of them to a 400.
var ErrInvalidStateOp = errors.New("tofu: invalid state operation")

// maxStateOpFieldLen bounds each field: an address, lock id, or import id
// is a short token, never a document.
const maxStateOpFieldLen = 1024

// Validate checks the operation is one of the fixed menu and that exactly
// the fields it needs are present and well-formed: single-line tokens with
// no whitespace, so a value can never smuggle a second argument or a shell
// construct (the script shell-quotes every field regardless — this is the
// user-facing check, not the security boundary).
func (o StateOp) Validate() error {
	check := func(name, v string, required bool) error {
		if v == "" {
			if required {
				return fmt.Errorf("%w: %s requires %s", ErrInvalidStateOp, o.Operation, name)
			}
			return nil
		}
		if len(v) > maxStateOpFieldLen {
			return fmt.Errorf("%w: %s is too long (at most %d characters)", ErrInvalidStateOp, name, maxStateOpFieldLen)
		}
		if strings.ContainsAny(v, " \t\r\n") {
			return fmt.Errorf("%w: %s must not contain whitespace", ErrInvalidStateOp, name)
		}
		return nil
	}
	var required, unused []struct{ name, v string }
	switch o.Operation {
	case StateOpForceUnlock:
		required = []struct{ name, v string }{{"lock_id", o.LockID}}
		unused = []struct{ name, v string }{{"address", o.Address}, {"new_address", o.NewAddress}, {"import_id", o.ImportID}}
	case StateOpRemove:
		required = []struct{ name, v string }{{"address", o.Address}}
		unused = []struct{ name, v string }{{"new_address", o.NewAddress}, {"lock_id", o.LockID}, {"import_id", o.ImportID}}
	case StateOpMove:
		required = []struct{ name, v string }{{"address", o.Address}, {"new_address", o.NewAddress}}
		unused = []struct{ name, v string }{{"lock_id", o.LockID}, {"import_id", o.ImportID}}
	case StateOpImport:
		required = []struct{ name, v string }{{"address", o.Address}, {"import_id", o.ImportID}}
		unused = []struct{ name, v string }{{"new_address", o.NewAddress}, {"lock_id", o.LockID}}
	case "":
		return fmt.Errorf("%w: operation is required", ErrInvalidStateOp)
	default:
		return fmt.Errorf("%w: unknown operation %q (one of %s, %s, %s, %s)", ErrInvalidStateOp, o.Operation, StateOpForceUnlock, StateOpRemove, StateOpMove, StateOpImport)
	}
	for _, f := range required {
		if err := check(f.name, f.v, true); err != nil {
			return err
		}
	}
	for _, f := range unused {
		if f.v != "" {
			return fmt.Errorf("%w: %s does not apply to %s", ErrInvalidStateOp, f.name, o.Operation)
		}
	}
	if o.Operation == StateOpMove && o.Address == o.NewAddress {
		return fmt.Errorf("%w: new_address must differ from address", ErrInvalidStateOp)
	}
	return nil
}

// Label is the short human name of the operation, for a run's step name and
// the run history ("state rm", "import", …).
func (o StateOp) Label() string {
	switch o.Operation {
	case StateOpForceUnlock:
		return "force-unlock"
	case StateOpRemove:
		return "state rm"
	case StateOpMove:
		return "state mv"
	case StateOpImport:
		return "import"
	default:
		return o.Operation
	}
}

// argv is the exact tofu argument vector the step runs for the operation,
// after `tofu init`. importFlags are the plan-time -var/-var-file flags an
// import needs so the resource's configuration resolves; the other
// operations take none. Shared by Script (which shell-quotes each token) and
// Command (the display form), so the approval gate shows the command that
// actually runs.
func (o StateOp) argv(importFlags []string) []string {
	switch o.Operation {
	case StateOpForceUnlock:
		// -force skips the interactive confirmation (force-unlock has no
		// -input flag).
		return []string{"tofu", "force-unlock", "-force", o.LockID}
	case StateOpRemove:
		return []string{"tofu", "state", "rm", o.Address}
	case StateOpMove:
		return []string{"tofu", "state", "mv", o.Address, o.NewAddress}
	case StateOpImport:
		args := []string{"tofu", "import", "-input=false", "-no-color"}
		args = append(args, importFlags...)
		return append(args, o.Address, o.ImportID)
	default:
		return nil
	}
}

// Command renders the operation as the one-line command the step will run,
// for display at the approval gate and in the run history. Tokens are quoted
// only when they need it, so the common case reads like what an operator
// would type. planFlags are the component's plan flags; an import carries the
// -var/-var-file ones (see ImportFlags), nothing else does.
func (o StateOp) Command(planFlags []string) string {
	argv := o.argv(ImportFlags(planFlags))
	tokens := make([]string, 0, len(argv))
	for _, t := range argv {
		if needsQuote(t) {
			t = shQuote(t)
		}
		tokens = append(tokens, t)
	}
	return strings.Join(tokens, " ")
}

// ImportFlags narrows a component's plan flags to the ones `tofu import`
// accepts and needs — the -var / -var-file tokens (in either their `=` form
// or as a flag followed by its value) — so the resource configuration
// resolves the same way it does for a plan, while plan-only flags (-target,
// -replace, -refresh, …) that import would reject are left out.
func ImportFlags(planFlags []string) []string {
	var out []string
	for i := 0; i < len(planFlags); i++ {
		f := planFlags[i]
		switch {
		case strings.HasPrefix(f, "-var=") || strings.HasPrefix(f, "-var-file="):
			out = append(out, f)
		case (f == "-var" || f == "-var-file") && i+1 < len(planFlags):
			out = append(out, f, planFlags[i+1])
			i++
		}
	}
	return out
}

// needsQuote reports whether a display token contains characters outside the
// set that reads unambiguously unquoted (letters, digits, and the punctuation
// common in addresses, ids, and flags).
func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-_./:=,[]\"@+~", r):
		default:
			return true
		}
	}
	return false
}
