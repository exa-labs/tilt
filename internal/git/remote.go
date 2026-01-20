package git

import (
	"os/exec"
	"strings"

	giturls "github.com/whilp/git-urls"
)

type GitRemote string

func (gr GitRemote) String() string {
	return string(gr)
}

func ProvideGitRemote() GitRemote {
	return GitRemote(normalizeGitRemote(gitOrigin(".")))
}

func gitOrigin(fromDir string) string {
	cmd := exec.Command("git", "-C", fromDir, "remote", "get-url", "origin")
	b, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimRight(string(b), "\n")
}

func normalizeGitRemote(s string) string {
	u, err := giturls.Parse(s)
	if err != nil {
		return s
	}

	// treat "http://", "https://", "git://", "ssh://", etc as equiv
	u.Scheme = ""
	u.User = nil

	// github.com/tilt-dev/tilt is the same as github.com/tilt-dev/tilt/
	u.Path = strings.TrimSuffix(u.Path, "/")
	// github.com/tilt-dev/tilt is the same as github.com/tilt-dev/tilt.git
	u.Path = strings.TrimSuffix(u.Path, ".git")

	return u.String()
}

func GetHeadCommit(fromDir string) string {
	cmd := exec.Command("git", "-C", fromDir, "rev-parse", "HEAD")
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}

// FetchCommit fetches a specific commit from origin. Useful for shallow clones
// where the commit may not exist locally (e.g., after rebase).
func FetchCommit(fromDir, commit string) error {
	cmd := exec.Command("git", "-C", fromDir, "fetch", "origin", commit, "--depth=1")
	return cmd.Run()
}

func GetDiffFiles(fromDir, fromCommit, toCommit string) ([]string, error) {
	cmd := exec.Command("git", "-C", fromDir, "diff", "--name-only", fromCommit, toCommit)
	b, err := cmd.Output()
	if err != nil {
		// Commit might not exist in shallow clone - try fetching it
		if fetchErr := FetchCommit(fromDir, fromCommit); fetchErr == nil {
			// Retry diff after fetching
			cmd = exec.Command("git", "-C", fromDir, "diff", "--name-only", fromCommit, toCommit)
			b, err = cmd.Output()
		}
		if err != nil {
			return nil, err
		}
	}
	output := strings.TrimRight(string(b), "\n")
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
}
