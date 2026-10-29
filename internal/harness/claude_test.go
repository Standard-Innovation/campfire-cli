package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectClaude(t *testing.T) {
	t.Run("detects via .claude directory", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !DetectClaude() {
			t.Error("expected DetectClaude() to return true when ~/.claude exists")
		}
	})

	t.Run("returns false when nothing present", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("PATH", home) // no claude binary
		if DetectClaude() {
			t.Error("expected DetectClaude() to return false")
		}
	})
}

func TestCheckClaudeSkillLink(t *testing.T) {
	t.Run("pass when skill file exists", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		skillDir := filepath.Join(home, ".claude", "skills", "campfire")
		os.MkdirAll(skillDir, 0o755)
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("content"), 0o644)

		check := CheckClaudeSkillLink()
		if check.Status != "pass" {
			t.Errorf("expected pass, got %s: %s", check.Status, check.Message)
		}
	})

	t.Run("fail when skill file missing", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		check := CheckClaudeSkillLink()
		if check.Status != "fail" {
			t.Errorf("expected fail, got %s: %s", check.Status, check.Message)
		}
	})
}
