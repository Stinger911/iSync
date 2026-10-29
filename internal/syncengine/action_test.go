package syncengine

import "testing"

func TestActionString(t *testing.T) {
	cases := map[Action]string{
		ActionNoOp:               "no-op",
		ActionUpload:             "upload",
		ActionDownload:           "download",
		ActionDeleteLocal:        "delete-local",
		ActionDeleteRemote:       "delete-remote",
		ActionConflictRemoteWins: "conflict-remote-wins",
		ActionConflictLocalWins:  "conflict-local-wins",
	}
	for action, want := range cases {
		if got := action.String(); got != want {
			t.Errorf("Action(%d).String() = %q, want %q", action, got, want)
		}
	}
}
