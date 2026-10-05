package core

import "strings"

const MaxRecoveryNonces = 16

func admitRecoveryNonces(op *Op, lim Limits) error {
	if op.RecoveryNonces == nil {
		return nil
	}
	if op.Kind != OpRegister || op.Nonce != "" || strings.TrimSpace(op.Name) == "" {
		return errf("E_BAD_ARG", "use recovery_nonces only on register with name and without an explicit or transport nonce",
			"recovery_nonces cannot be combined with this registration")
	}
	if len(op.RecoveryNonces) < 2 || len(op.RecoveryNonces) > MaxRecoveryNonces {
		return errf("E_BAD_ARG", "supply 2 to 16 retained credentials, or register with one explicit nonce",
			"recovery_nonces needs 2 to 16 candidates")
	}
	if err := boundStrings(lim.MaxIDBytes, "recovery_nonces", op.RecoveryNonces); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, nonce := range op.RecoveryNonces {
		if strings.TrimSpace(nonce) == "" || seen[nonce] {
			return errf("E_BAD_ARG", "deduplicate retained credentials and omit empty values", "invalid recovery_nonces group")
		}
		seen[nonce] = true
	}
	return nil
}
