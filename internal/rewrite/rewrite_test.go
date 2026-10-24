package rewrite

import "testing"

func TestTextBoundaries(t *testing.T) {
	cases := []struct {
		in, want string
		n        int
	}{
		{"/ws/a", "/ws/b", 1},
		{"see /ws/a/x.go now", "see /ws/b/x.go now", 1},
		{"/ws/ab is different", "/ws/ab is different", 0},
		{"/x/ws/a is different", "/x/ws/a is different", 0},
		{"file /ws/a.", "file /ws/b.", 1},
		{"/ws/a.txt is a file", "/ws/a.txt is a file", 0},
		{"`/ws/a` and (/ws/a/c)", "`/ws/b` and (/ws/b/c)", 2},
		{"file:///ws/a/readme", "file:///ws/b/readme", 1},
		{"/ws/a-old stays", "/ws/a-old stays", 0},
	}
	for _, c := range cases {
		got, n := Text([]byte(c.in), "/ws/a", "/ws/b")
		if string(got) != c.want || n != c.n {
			t.Errorf("Text(%q) = %q, %d; want %q, %d", c.in, got, n, c.want, c.n)
		}
	}
}

func TestJSON(t *testing.T) {
	in := `{"cwd":"/ws/a","file":"/ws/a/x","other":"/ws/abc","t":"line\n/ws/a\nmore"}`
	want := `{"cwd":"/ws/new dir","file":"/ws/new dir/x","other":"/ws/abc","t":"line\n/ws/new dir\nmore"}`
	got, n := JSON([]byte(in), "/ws/a", "/ws/new dir")
	if string(got) != want || n != 3 {
		t.Errorf("JSON = %s, %d; want %s, 3", got, n, want)
	}
}

func TestMoveIntoSubdirOfItself(t *testing.T) {
	got, n := Text([]byte("/ws/a and /ws/a/b"), "/ws/a", "/ws/a/inner")
	if string(got) != "/ws/a/inner and /ws/a/inner/b" || n != 2 {
		t.Errorf("got %q, %d", got, n)
	}
}
