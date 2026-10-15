package delivery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseVerdict(t *testing.T) {
	for _, tc := range []struct {
		report string
		want   Verdict
		reason bool
	}{
		{"all good\nVERDICT: PASS", VerdictPass, false},
		{"all good\nVERDICT: PASS\n", VerdictPass, false},
		{"**VERDICT: FAIL**", VerdictFail, false},
		{"`VERDICT: skipped`", VerdictSkipped, false},
		{"Verdict: pass", VerdictPass, false},
		// The last verdict line wins, so a report quoting an earlier verdict
		// is judged on its own conclusion.
		{"> VERDICT: FAIL\nfixed now\nVERDICT: PASS", VerdictPass, false},
		{"pass or fail: pass", VerdictFail, true},
		{"", VerdictFail, true},
	} {
		got, reason := ParseVerdict(tc.report)
		require.Equal(t, tc.want, got, tc.report)
		require.Equal(t, tc.reason, reason != "", tc.report)
	}
}

func TestLastMessage(t *testing.T) {
	out := "Sync \"https://example\": git pull: exit status 1\n" +
		`{"session":{"id":"s1","name":"n"}}` + "\n" +
		`{"reply":{"content":{"text":"hi\nVERDICT: PASS"}},"session":{"id":"s1","name":"n"},"status":{"state":"STOPPED"},"stop":{}}` + "\n"
	m, ok := lastMessage(out)
	require.True(t, ok)
	require.True(t, m.stopped())
	require.Equal(t, "hi\nVERDICT: PASS", m.Reply.Content.Text)

	_, ok = lastMessage(`{"session":{"id":"s1","name":"n"}}`)
	require.False(t, ok)
}

func TestDefaults(t *testing.T) {
	in := withDefaults(DeliveryInput{Request: "x"})
	require.Equal(t, DefaultTemplate, in.Template)
	require.Equal(t, DefaultWorkspace, in.Workspace)
	require.Equal(t, DefaultInterceptPlan, in.InterceptPlan)
	require.Equal(t, DefaultMaxAttempts, in.MaxAttempts)

	in = withDefaults(DeliveryInput{Request: "x", InterceptPlan: "None"})
	require.Empty(t, in.InterceptPlan)
	require.True(t, in.SkipCluster)
}
