package core

import "fmt"

// AI policies of a space (DECISIONS, Q-11).
const (
	AIOff   = "off"
	AILocal = "local"
	AIAny   = "any"
)

// AIPolicy returns the space's policy. A space that says nothing is "local":
// its text never goes to a cloud model unless someone decides so.
func (sp *Space) AIPolicy() string {
	switch sp.AI {
	case AIOff, AIAny:
		return sp.AI
	}
	return AILocal
}

// AIAllows says whether text of the space may be sent to a backend that is
// local (runs on the operator's machines) or not.
func (sp *Space) AIAllows(local bool) bool {
	switch sp.AIPolicy() {
	case AIAny:
		return true
	case AILocal:
		return local
	}
	return false
}

// ValidAIPolicy checks a policy value set by a user.
func ValidAIPolicy(p string) error {
	if p != AIOff && p != AILocal && p != AIAny {
		return fmt.Errorf("%w: AI policy %q (want off, local or any)", ErrInvalid, p)
	}
	return nil
}
