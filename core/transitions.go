package core

import "slices"

// transitions lists, per kind, the state changes a user may request (SPEC §6.1).
// `planned` is never a target: it is set and cleared only by the dependency rule.
var transitions = map[string]map[string][]string{
	KindCapture: {
		StInbox:   {StTrashed},
		StTrashed: {StInbox},
	},
	KindAction: {
		StNext:    {StWaiting, StSomeday, StDone, StDropped, StTrashed},
		StWaiting: {StNext, StSomeday, StDone, StDropped, StTrashed},
		StSomeday: {StNext, StWaiting, StDone, StDropped, StTrashed},
		StPlanned: {StDone, StDropped, StTrashed},
		StDone:    {StNext},
		StDropped: {StNext},
		StTrashed: {StNext},
	},
	KindProject: {
		StActive:  {StSomeday, StDone, StDropped, StTrashed},
		StSomeday: {StActive, StDone, StDropped, StTrashed},
		StDone:    {StActive},
		StDropped: {StActive},
		StTrashed: {StActive},
	},
	KindNote: {
		StUnsorted: {StFiled, StArchived, StTrashed},
		StFiled:    {StUnsorted, StArchived, StTrashed},
		StArchived: {StUnsorted, StFiled, StTrashed},
		StTrashed:  {StUnsorted},
	},
	KindArea: {
		StActive:  {StRetired},
		StRetired: {StActive},
	},
}

// initial lists the states an item of each kind may be created in. The same
// states are the entry points when a capture is clarified into that kind.
var initial = map[string][]string{
	KindCapture: {StInbox},
	KindAction:  {StNext, StPlanned, StWaiting, StSomeday},
	KindProject: {StActive, StSomeday},
	KindNote:    {StUnsorted, StFiled},
	KindArea:    {StActive},
}

// CanTransition reports whether a user may move an item of kind from one state
// to another.
func CanTransition(kind, from, to string) bool {
	return slices.Contains(transitions[kind][from], to)
}

// CanClarify reports whether a capture in the inbox may become an item of the
// given kind and state.
func CanClarify(kind, state string) bool {
	return kind != KindCapture && kind != KindArea && state != StPlanned &&
		slices.Contains(initial[kind], state)
}

// resolved reports whether a blocker in this state no longer blocks (§6.2).
func resolved(state string) bool {
	return state == StDone || state == StDropped || state == StTrashed
}

// validState reports whether state exists for kind.
func validState(kind, state string) bool {
	_, ok := transitions[kind][state]
	return ok
}
