package service

import "fmt"

// Identity access rules. A personal identity belongs to identities.user_id. A
// shared identity's user_id is only its steward; members reach it through
// shared_mailbox_members, never through user_id.
const (
	identityCanRead   = "can_read"
	identityCanSend   = "can_send"
	identityCanManage = "can_manage"
)

// identityAccessSQL returns a predicate that is true when the user given by the
// SQL expression user may use the identity aliased alias: a personal identity
// the user owns, or a shared identity whose linked mailbox grants the user perm.
func identityAccessSQL(alias, user, perm string) string {
	switch perm {
	case identityCanRead, identityCanSend, identityCanManage:
	default:
		panic("identityAccessSQL: unknown permission " + perm)
	}
	return fmt.Sprintf(`((%[1]s.kind='personal' AND %[1]s.user_id=%[2]s) OR (%[1]s.kind='shared' AND EXISTS(
		SELECT 1 FROM shared_mailboxes sm JOIN shared_mailbox_members m ON m.shared_mailbox_id=sm.id
		WHERE sm.identity_id=%[1]s.id AND m.user_id=%[2]s AND m.%[3]s)))`, alias, user, perm)
}
