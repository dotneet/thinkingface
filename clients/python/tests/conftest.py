"""Shared fixtures for the clients/python suite."""

from __future__ import annotations

import pytest


@pytest.fixture(autouse=True)
def _offline_dir_in_tmp(monkeypatch, tmp_path):
    """Keep every run directory the shim writes inside the test's tmp_path.

    The online mode spills points it gives up on to THINKINGFACE_OFFLINE_DIR
    (default ./thinkingface-offline), and several suites deliberately make a
    run give up -- without this they would litter the working directory. The
    mode and the timing knobs are cleared so a developer's shell cannot
    change what the tests exercise.
    """
    monkeypatch.setenv("THINKINGFACE_OFFLINE_DIR", str(tmp_path / "thinkingface-offline"))
    for name in (
        "THINKINGFACE_MODE",
        "THINKINGFACE_HEARTBEAT_SECS",
        "THINKINGFACE_ARTIFACT_INTERVAL",
    ):
        monkeypatch.delenv(name, raising=False)
