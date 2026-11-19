package core

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Repeating actions. An action's `repeat` says when it comes back after it
// was done:
//
//	"3d", "2w"      three days, or two weeks, after the day it was done
//	"mon,wed,fri"   on the next of these weekdays after the day it was done
//
// When a repeating action is completed, the rule creates its successor as a
// system op in the same batch: a copy, deferred until that date. The tickler
// brings it back on the day. There is only ever one open instance: skipping a
// day does not pile up copies.

var weekdayNames = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// ParseRepeat checks a repeat value and returns it in canonical form (weekdays
// in week order, without duplicates). The empty string means "does not repeat".
func ParseRepeat(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if unit := s[len(s)-1]; unit == 'd' || unit == 'w' {
		if n, err := strconv.Atoi(s[:len(s)-1]); err == nil && n >= 1 && n <= 365 {
			return strconv.Itoa(n) + string(unit), nil
		}
	}
	var days []int
	for _, part := range strings.Split(s, ",") {
		i := slices.Index(weekdayNames, strings.TrimSpace(part))
		if i < 0 {
			return "", fmt.Errorf("%w: repeat %q (want e.g. 2d, 1w or mon,wed,fri)", ErrInvalid, s)
		}
		if !slices.Contains(days, i) {
			days = append(days, i)
		}
	}
	slices.Sort(days)
	names := make([]string, len(days))
	for i, d := range days {
		names[i] = weekdayNames[d]
	}
	return strings.Join(names, ","), nil
}

// NextRepeat returns the date a repeating action comes back, given the date
// (YYYY-MM-DD) it was done. repeat must be canonical and not empty.
func NextRepeat(repeat, done string) string {
	t, err := time.Parse(time.DateOnly, done)
	if err != nil {
		return done
	}
	if unit := repeat[len(repeat)-1]; unit == 'd' || unit == 'w' {
		if n, err := strconv.Atoi(repeat[:len(repeat)-1]); err == nil {
			if unit == 'w' {
				n *= 7
			}
			return t.AddDate(0, 0, n).Format(time.DateOnly)
		}
	}
	days := strings.Split(repeat, ",")
	for i := 1; i <= 7; i++ {
		next := t.AddDate(0, 0, i)
		if slices.Contains(days, weekdayNames[(int(next.Weekday())+6)%7]) {
			return next.Format(time.DateOnly)
		}
	}
	return done
}

// repeatRule creates the successor of a repeating action that was just
// completed. Dropping or trashing ends the repetition. It returns the ID of
// the item it created, or "".
func repeatRule(ws *State, op Op, before, after *Item, newID func() string, res *Result) string {
	if after.Kind != KindAction || after.Repeat == "" || after.State != StDone || before == nil || before.State == StDone {
		return ""
	}
	next := Item{ID: newID(), Kind: KindAction, State: StNext, Title: after.Title, Body: after.Body,
		Project: after.Project, Area: after.Area, Context: after.Context, Owner: after.Owner,
		Energy: after.Energy, Time: after.Time, Repeat: after.Repeat, Template: after.Template, Fields: after.Fields,
		Source: Source{Via: System}, DerivedFrom: []string{after.ID},
		DeferUntil: NextRepeat(after.Repeat, op.TS.Format(time.DateOnly))}
	d := Op{V: Version, ID: newID(), Space: op.Space, TS: op.TS, Actor: System, Via: System, Type: OpCreate, Payload: mustJSON(next)}
	if err := prepareCreate(ws, &d); err != nil {
		return "" // cannot happen for a copy of a valid action; never fail the completion over it
	}
	it, err := replayItem(nil, d)
	if err != nil {
		return ""
	}
	ws.Pending[it.ID] = it
	res.Ops = append(res.Ops, d)
	return it.ID
}
