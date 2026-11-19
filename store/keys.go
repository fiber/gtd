package store

import (
	"encoding/binary"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// Keyspace (STORAGE §2.1), M0 subset:
//
//	m/schema                        {"v": N}
//	m/server                        Server
//	u/<user>                        core.User
//	u/<user>/n/<seq:be64>           core.Notification
//	sp/<space>                      core.Space
//	sp/<space>/it/<itemid>          core.Item
//	sp/<space>/cm/<itemid>/<cmtid>  core.Comment
//	sp/<space>/op/<seq:be64>        core.Op
//	sp/<space>/seq                  last op seq, be64
//	opid/<opid>                     opRef
//	sess/<id>                       Session (M1)
//	x/vec/<space>/<itemid>          embedding: 32-byte text hash, passage count, item vector, passage vectors (LE float32)
//	x/import/<space>/<sha1(path)>   import: {id, hash} of the file imported from that path (M5)
//	x/import/<space>/report         the last import's report (M5)
//	x/vecid                         identity of the stored vectors (M4)
//	x/pdf/<sha256>                  extracted PDF text (M4)
//
// Everything under x/ is derived: it is rebuilt from the items, never exported,
// and its values are binary or plain text, not versioned JSON.
//
// User and space IDs match core.ValidID, so they never contain "/".

const (
	keySchema = "m/schema"
	keyServer = "m/server"
)

func be64(n uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	return string(b[:])
}

func userKey(user string) []byte                  { return []byte("u/" + user) }
func notifPrefix(user string) string              { return "u/" + user + "/n/" }
func notifKey(user string, seq uint64) []byte     { return []byte(notifPrefix(user) + be64(seq)) }
func spaceKey(space string) []byte                { return []byte("sp/" + space) }
func itemPrefix(space string) string              { return "sp/" + space + "/it/" }
func itemKey(space, id string) []byte             { return []byte(itemPrefix(space) + id) }
func commentPrefix(space string) string           { return "sp/" + space + "/cm/" }
func opPrefix(space string) string                { return "sp/" + space + "/op/" }
func opKey(space string, seq uint64) []byte       { return []byte(opPrefix(space) + be64(seq)) }
func seqKey(space string) []byte                  { return []byte("sp/" + space + "/seq") }
func opidKey(opid string) []byte                  { return []byte("opid/" + opid) }
func sessionKey(id string) []byte                 { return []byte("sess/" + id) }
func vecPrefix(space string) string               { return "x/vec/" + space + "/" }
func vecKey(space, id string) []byte              { return []byte(vecPrefix(space) + id) }
func pdfKey(sha string) []byte                    { return []byte("x/pdf/" + sha) }
func commentItemPrefix(space, item string) string { return commentPrefix(space) + item + "/" }
func commentKey(space, item, id string) []byte {
	return []byte(commentPrefix(space) + item + "/" + id)
}

// prefixEnd returns the smallest key greater than every key with the prefix.
func prefixEnd(prefix string) []byte {
	b := []byte(prefix)
	b[len(b)-1]++
	return b
}

// scan calls f for every key with the prefix, in key order. The slices passed
// to f are only valid during the call.
func scan(r pebble.Reader, prefix string, f func(key string, value []byte) error) error {
	it, err := r.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: prefixEnd(prefix)})
	if err != nil {
		return err
	}
	for it.First(); it.Valid(); it.Next() {
		if err := f(string(it.Key()), it.Value()); err != nil {
			it.Close()
			return err
		}
	}
	if err := it.Error(); err != nil {
		it.Close()
		return err
	}
	return it.Close()
}

// spaceIDs returns the IDs of all spaces in key order. It hops from one space
// record to the next without reading the items, comments and ops in between.
func spaceIDs(r pebble.Reader) ([]string, error) {
	it, err := r.NewIter(&pebble.IterOptions{LowerBound: []byte("sp/"), UpperBound: prefixEnd("sp/")})
	if err != nil {
		return nil, err
	}
	var ids []string
	for ok := it.First(); ok; {
		rest := string(it.Key())[len("sp/"):]
		if id, _, sub := strings.Cut(rest, "/"); sub {
			// A key inside space id: skip to the first key after "sp/<id>/".
			ok = it.SeekGE(prefixEnd("sp/" + id + "/"))
		} else {
			ids = append(ids, rest)
			ok = it.Next()
		}
	}
	if err := it.Error(); err != nil {
		it.Close()
		return nil, err
	}
	return ids, it.Close()
}

// userIDs returns the IDs of all users in key order.
func userIDs(r pebble.Reader) ([]string, error) {
	var ids []string
	err := scan(r, "u/", func(key string, _ []byte) error {
		if rest := key[len("u/"):]; !strings.Contains(rest, "/") {
			ids = append(ids, rest)
		}
		return nil
	})
	return ids, err
}
