package domain

// Actor is who performs an operation, with their level in the entry's space.
type Actor struct {
	Principal Principal
	Level     Level
}

// transitions lists allowed status changes and the minimum requirement for each.
type requirement int

const (
	anyWriter  requirement = iota // write level is enough, agents included
	humanOnly                     // write level, but the principal must not be an agent
	reviewOnly                    // review level (agents never get it)
)

var transitions = map[Status]map[Status]requirement{
	StatusDraft: {
		StatusProposed: anyWriter,
		StatusActive:   humanOnly,
		StatusArchived: anyWriter,
	},
	StatusProposed: {
		StatusActive:   reviewOnly,
		StatusArchived: reviewOnly,
		StatusDraft:    anyWriter,
	},
	StatusActive: {
		StatusDeprecated: anyWriter,
		StatusSuperseded: anyWriter,
		StatusArchived:   humanOnly,
	},
	StatusDeprecated: {
		StatusActive:   humanOnly,
		StatusArchived: humanOnly,
	},
	StatusSuperseded: {
		StatusArchived: humanOnly,
	},
	StatusArchived: {
		StatusActive: reviewOnly,
	},
}

// CheckTransition validates a status change. Agents can never approve, restore or archive
// confirmed knowledge; they can deprecate or supersede it, which stays visible in history.
func CheckTransition(from, to Status, a Actor) error {
	req, ok := transitions[from][to]
	if !ok {
		return InvalidTransition(from, to)
	}
	switch req {
	case humanOnly:
		if a.Principal.IsAgent() {
			return Forbidden("agents cannot change status from %s to %s", from, to)
		}
	case reviewOnly:
		if a.Level < LevelReview || a.Principal.IsAgent() {
			return Forbidden("changing status from %s to %s requires review permission", from, to)
		}
	}
	return nil
}

// InitialStatus decides the status of a new entry.
func InitialStatus(draft bool, a Actor, requiresReview bool) Status {
	switch {
	case draft:
		return StatusDraft
	case a.Principal.IsAgent() && requiresReview:
		return StatusProposed
	default:
		return StatusActive
	}
}

// RequiresReview reports whether an agent-written entry of this kind in this space needs review.
func RequiresReview(k Kind, sp Space) bool {
	if sp.Settings.AgentAlwaysReview {
		return true
	}
	if sp.Settings.ReviewKinds != nil {
		for _, rk := range sp.Settings.ReviewKinds {
			if rk == k.Kind {
				return true
			}
		}
		return false
	}
	return k.AgentRequiresReview
}
