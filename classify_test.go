package grok

import "testing"

func TestPromptText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fix the   bug\nplease", "fix the bug please"},
		{"<user_query>real   question</user_query>", "real question"},
		{"<user_info>OS: linux</user_info>", ""},
		{"<system_reminder>x</system_reminder>", ""},
		{"<agent_skills>x</agent_skills>", ""},
		{"<rules>always x</rules>", ""},
		{"<command-name>/verify</command-name>", ""},
		{"/compact", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := promptText(c.in); got != c.want {
			t.Errorf("promptText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
