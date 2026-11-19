package core

import "slices"

// HasMember reports whether user may read and write the space. Membership is
// the only permission (D-11).
func (sp *Space) HasMember(user string) bool {
	return user != "" && slices.Contains(sp.Members, user)
}

// CanAct reports whether actor may apply ops in the space: members, and the
// system actor for rule-driven ops.
func CanAct(sp *Space, actor string) bool {
	return actor == System || sp.HasMember(actor)
}

// ValidID reports whether s is usable as a user or space ID: [a-z0-9][a-z0-9-]*,
// at most 64 bytes. This keeps "/" free as the key separator and IDs safe as
// directory names in exports.
func ValidID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' && i > 0
		if !ok {
			return false
		}
	}
	return true
}

// Reserved reports whether id is reserved for built-in actors.
func Reserved(id string) bool { return id == System || id == "ai" }
