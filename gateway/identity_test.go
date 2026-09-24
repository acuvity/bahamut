package gateway

import "testing"

// However a resource is addressed, the gateway must resolve the identity and
// prefix the upstreamer routes it by.
func TestTargetIdentity(t *testing.T) {

	for _, c := range []struct {
		path     string
		identity string
		prefix   string
	}{
		{"/", "", ""},
		{"/users", "users", ""},
		{"/users/id", "users", ""},
		{"/users/id/groups", "groups", ""},
		{"/v/1/users", "users", ""},
		{"/v/1/users/id", "users", ""},
		{"/v/1/users/id/groups", "groups", ""},
		{"/v/12/analyze", "analyze", ""},
		{"/analyze/inline", "analyze", ""},

		{"_prefix/", "", "prefix"},
		{"_prefix/users", "users", "prefix"},
		{"_prefix/users/id", "users", "prefix"},
		{"_prefix/users/id/groups", "groups", "prefix"},
		{"_prefix/v/1/users", "users", "prefix"},
		{"_prefix/v/1/users/id", "users", "prefix"},
		{"_prefix/v/1/users/id/groups", "groups", "prefix"},
	} {
		t.Run(c.path, func(t *testing.T) {
			identity, prefix := TargetIdentity(c.path)
			if identity != c.identity {
				t.Errorf("TargetIdentity(%q) identity = %q, want %q", c.path, identity, c.identity)
			}
			if prefix != c.prefix {
				t.Errorf("TargetIdentity(%q) prefix = %q, want %q", c.path, prefix, c.prefix)
			}
		})
	}
}
