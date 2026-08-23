"""Tests for thinkingface._env_meta.

Focused primarily on `mask_cmdline`, since a leaked --token/--password value
in a stored run's config is the concrete failure mode the masking exists to
prevent (see todo/exp-run-env-metadata.md). Also covers is_disabled() and
that collect() degrades to {} instead of raising when git/torch/nvidia-smi
are unavailable.
"""

from __future__ import annotations

import subprocess

import pytest

from thinkingface import _env_meta


class TestMaskCmdline:
    def test_leaves_plain_arguments_untouched(self):
        argv = ["train.py", "--epochs", "10", "positional"]
        assert _env_meta.mask_cmdline(argv) == argv

    def test_masks_two_argument_form(self):
        argv = ["train.py", "--token", "sk-super-secret"]
        assert _env_meta.mask_cmdline(argv) == ["train.py", "--token", "***"]

    def test_masks_equals_form(self):
        argv = ["train.py", "--api-key=abcdef123456"]
        assert _env_meta.mask_cmdline(argv) == ["train.py", "--api-key=***"]

    @pytest.mark.parametrize(
        "flag",
        [
            "--token",
            "--access-token",
            "--hf-token",
            "--auth-token",
            "--password",
            "--passwd",
            "--pwd",
            "--secret",
            "--secret-key",
            "--api-key",
            "--api_key",
            "--apikey",
            "--credential",
            "--credentials",
            "--access-key",
            "--private-key",
            "--auth",
        ],
    )
    def test_masks_known_sensitive_flag_names(self, flag):
        argv = ["train.py", flag, "value-that-must-not-leak"]
        result = _env_meta.mask_cmdline(argv)
        assert "value-that-must-not-leak" not in result
        assert result[-1] == "***"

    def test_masks_single_word_compound_flag(self):
        # No separator at all -- still must be caught.
        argv = ["train.py", "--apikey", "shh"]
        assert _env_meta.mask_cmdline(argv) == ["train.py", "--apikey", "***"]

    def test_case_insensitive(self):
        argv = ["train.py", "--TOKEN", "shh"]
        assert _env_meta.mask_cmdline(argv) == ["train.py", "--TOKEN", "***"]

    def test_does_not_mask_unrelated_flags(self):
        argv = ["train.py", "--learning-rate", "0.001", "--keyword", "hello"]
        assert _env_meta.mask_cmdline(argv) == argv

    def test_sensitive_flag_as_last_argument_has_no_value_to_mask(self):
        # Malformed / boolean-style usage: no trailing value to consume.
        argv = ["train.py", "--token"]
        assert _env_meta.mask_cmdline(argv) == ["train.py", "--token"]

    def test_does_not_touch_positional_arguments_that_look_like_secrets(self):
        # A bare positional value (no leading '-') is never masked, even if
        # it happens to contain a sensitive-looking substring.
        argv = ["train.py", "my-token-file.txt"]
        assert _env_meta.mask_cmdline(argv) == argv

    def test_empty_argv(self):
        assert _env_meta.mask_cmdline([]) == []


class TestIsDisabled:
    def test_unset_is_enabled(self, monkeypatch):
        monkeypatch.delenv("THINKINGFACE_META", raising=False)
        assert _env_meta.is_disabled() is False

    def test_off_disables(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_META", "off")
        assert _env_meta.is_disabled() is True

    def test_off_is_case_insensitive_and_trims_whitespace(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_META", " OFF ")
        assert _env_meta.is_disabled() is True

    def test_other_values_are_enabled(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_META", "on")
        assert _env_meta.is_disabled() is False


class TestCollectBestEffort:
    def test_collect_never_raises_when_git_is_missing(self, monkeypatch):
        def _raise(*args, **kwargs):
            raise FileNotFoundError("git not found")

        monkeypatch.setattr(_env_meta.subprocess, "run", _raise)
        meta = _env_meta.collect()
        assert "git" not in meta
        assert "gpu" not in meta

    def test_collect_never_raises_when_everything_fails(self, monkeypatch):
        def _raise(*args, **kwargs):
            raise RuntimeError("boom")

        monkeypatch.setattr(_env_meta.subprocess, "run", _raise)
        monkeypatch.setattr(_env_meta.socket, "gethostname", _raise)
        monkeypatch.setattr(_env_meta.platform, "platform", _raise)
        monkeypatch.setattr(
            _env_meta, "_collect_requirements_sha256", lambda: (_ for _ in ()).throw(RuntimeError())
        )

        meta = _env_meta.collect()  # must not raise
        assert isinstance(meta, dict)

    def test_collect_git_outside_a_repository(self, tmp_path, monkeypatch):
        monkeypatch.chdir(tmp_path)
        info = _env_meta._collect_git()
        assert info == {}

    def test_collect_git_handles_missing_binary(self, monkeypatch):
        def _raise(*args, **kwargs):
            raise FileNotFoundError("no such file: git")

        monkeypatch.setattr(_env_meta.subprocess, "run", _raise)
        assert _env_meta._collect_git() == {}

    def test_collect_git_handles_timeout(self, monkeypatch):
        def _raise(*args, **kwargs):
            raise subprocess.TimeoutExpired(cmd="git", timeout=3)

        monkeypatch.setattr(_env_meta.subprocess, "run", _raise)
        assert _env_meta._collect_git() == {}

    def test_collect_includes_python_and_platform_by_default(self):
        meta = _env_meta.collect()
        # These should virtually always succeed in a normal test environment.
        assert "python" in meta
        assert "cmdline" in meta
