package task

import "testing"

func TestCanonicalSortsKeysAndEscapesLikeRFC8785(t *testing.T) {
	v := map[string]any{
		"b":      []any{int64(1), "x", true, nil},
		"a":      "tab\there \"q\" \\ \u0001 \u2028 é",
		"\u20ac": int64(-5),
		"\ufffd": int64(2),
		"😀":      int64(3),
		"A":      map[string]any{},
		"list":   []string{},
	}
	got, err := Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	// UTF-16 order: "A" < "a" < "b" < "list" < U+20AC < U+1F600 (D83D) < U+FFFD.
	want := `{"A":{},"a":"tab\there \"q\" \\ \u0001 ` + "\u2028" + ` é","b":[1,"x",true,null],"list":[],"€":-5,"😀":3,"` + "\ufffd" + `":2}`
	if string(got) != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestCanonicalRefusesWhatItCannotRepresentExactly(t *testing.T) {
	for name, v := range map[string]any{
		"float":    1.5,
		"big int":  int64(1 << 53),
		"bad utf8": string([]byte{0xff}),
		"struct":   struct{}{},
	} {
		if _, err := Canonical(v); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
