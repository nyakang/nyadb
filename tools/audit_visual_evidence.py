#!/usr/bin/env python3
"""Read-only preflight for genuine, single-sided NyaDB/Navicat capture evidence.

Does not run either application, access databases, or infer unknown capture
parameters. A side-ready result is NOT a differential (V) result.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
from pathlib import Path
import re

from PIL import Image


_DATABASE_SCENES = {"objects", "structure", "data"}
_ALL_SCENES = _DATABASE_SCENES | {"workbench", "connection"}
_REQUIRED_DB = ("database_host_port", "server_identity", "fixture_fingerprint",
                "rows_fingerprint", "schema", "read_status")
_REQUIRED_STATE = ("database_backed", "loading", "selection", "expanded", "menu_state")


def _known(value):
    return isinstance(value, str) and value.strip().lower() not in {
        "", "unknown", "n/a", "none", "unverified", "not measured"
    }


def _positive(value):
    return (isinstance(value, (int, float)) and not isinstance(value, bool)
            and math.isfinite(value) and value > 0)


def _local_file(base: Path, name: object) -> Path | None:
    """Avoid accidentally trusting files outside the capture evidence directory."""
    if not isinstance(name, str) or not name or Path(name).is_absolute():
        return None
    target = (base / name).resolve()
    if not target.is_relative_to(base.resolve()):
        return None
    return target if target.is_file() else None


def audit(path: Path, expected_application: str = "") -> dict:
    report = {"record": str(path), "status": "BLOCKED",
              "verified": [], "blockers": []}
    problems = report["blockers"]
    verified = report["verified"]
    try:
        record = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        problems.append("capture record is absent, unreadable or invalid JSON: " + str(exc))
        return report
    if not isinstance(record, dict):
        problems.append("capture record must be an object")
        return report
    base = path.resolve().parent
    scene = record.get("scene")
    if scene not in _ALL_SCENES:
        problems.append("scene: unsupported or absent")
    is_db = scene in _DATABASE_SCENES
    report["scene"] = scene
    app = record.get("application")
    if expected_application and app != expected_application:
        problems.append(f"application: expected {expected_application!r}, recorded {app!r}")
    elif _known(app):
        verified.append("capture declares application identity (not independently authenticated)")
    else:
        problems.append("application: missing identity")
    if not _known(record.get("source_version")):
        problems.append("source_version: missing/unknown executable identity")
    elif not re.fullmatch(r"sha256:[0-9a-f]{64}", record["source_version"]):
        problems.append("source_version: expected measured SHA-256 executable digest")
    else:
        verified.append("record contains SHA-256 executable identity (not independently authenticated)")

    screenshot = _local_file(base, record.get("screenshot"))
    if screenshot is None:
        problems.append("screenshot: missing or outside observation directory")
    else:
        digest = hashlib.sha256(screenshot.read_bytes()).hexdigest()
        report["screenshot_sha256_actual"] = digest
        if record.get("screenshot_sha256") != digest:
            problems.append("screenshot_sha256: capture JSON does not match screenshot bytes")
        else:
            verified.append("screenshot SHA-256 matches captured bytes")
        try:
            with Image.open(screenshot) as pic:
                width, height = pic.size
                pic.verify()
            report["desktop_dimensions"] = [width, height]
            crop = record.get("client_crop")
            if (not isinstance(crop, list) or len(crop) != 4
                    or any(type(n) is not int for n in crop)
                    or not (0 <= crop[0] < crop[2] <= width
                            and 0 <= crop[1] < crop[3] <= height)):
                problems.append("client_crop: outside actual PNG or invalid")
            elif [crop[2] - crop[0], crop[3] - crop[1]] != [
                record.get("viewport_width"), record.get("viewport_height")
            ]:
                problems.append("client_crop: client dimensions do not match declared viewport")
            else:
                verified.append("PNG dimensions, client crop and viewport match")
        except (OSError, ValueError) as exc:
            problems.append("screenshot: unreadable image: " + str(exc))

    evidence = record.get("observation_evidence")
    if (not isinstance(evidence, list) or not evidence
            or any(_local_file(base, name) is None for name in evidence)
            or path.name not in evidence):
        problems.append("observation_evidence: missing local observation record/files")
    else:
        verified.append("capture observation files exist and include this record")

    if record.get("render_ready") is not True:
        problems.append("render_ready: actual GUI stable frame not confirmed")
    else:
        stable = record.get("render_stability")
        if (not isinstance(stable, dict)
                or not isinstance(stable.get("consecutive_stable_comparisons"), int)
                or stable["consecutive_stable_comparisons"] < 3):
            problems.append("render_stability: insufficient stable frame measurements")
        else:
            verified.append("capture reports at least three stable frame comparisons")

    for field in ("dpi_x", "dpi_y", "actual_scale", "font_size_dip"):
        if not _positive(record.get(field)):
            problems.append(f"{field}: independently measured positive value required")
    if not _known(record.get("font_family")):
        problems.append("font_family: actually resolved font required")
    for field in ("platform", "locale", "theme"):
        if not _known(record.get(field)):
            problems.append(f"{field}: missing")
    if all(_positive(record.get(k)) for k in
           ("dpi_x", "dpi_y", "actual_scale", "font_size_dip")) and _known(record.get("font_family")):
        # Values still require independent cross-client verification.
        verified.append("record includes declared DPI, actual scale and resolved font")

    state = record.get("ui_state")
    if not isinstance(state, dict) or any(k not in state for k in _REQUIRED_STATE):
        problems.append("ui_state: selection, expanded, loading and menu evidence required")
    else:
        if state.get("database_backed") is not is_db:
            problems.append("ui_state.database_backed: inconsistent with scene")
        if state.get("loading") is not False:
            problems.append("ui_state.loading: scene is not settled")
        for key in ("selection", "expanded"):
            if not isinstance(state[key], list) or any(not isinstance(v, str) for v in state[key]):
                problems.append(f"ui_state.{key}: expected identity list")
        if not _known(state.get("menu_state")):
            problems.append("ui_state.menu_state: missing")

    if is_db:
        for field in _REQUIRED_DB:
            value = record.get(field)
            if not _known(value) or (field == "read_status" and value != "success"):
                problems.append(f"{field}: live database identity/read fingerprint not proven")
        if scene in {"structure", "data"} and not _known(record.get("table")):
            problems.append("table: queried object not proven")
        if scene == "data":
            if not _known(record.get("selected_column")):
                problems.append("selected_column: no verified active field")
            if not isinstance(record.get("visible_columns"), list) or not record["visible_columns"]:
                problems.append("visible_columns: no verified column order")
            if not isinstance(record.get("sort_rules"), list):
                problems.append("sort_rules: ordered SQL sorting state not proven")
    report["status"] = "SIDE_READY" if not problems else "BLOCKED"
    report["note"] = ("SIDE_READY only confirms internally consistent provided evidence; "
                      "it does not authenticate the observation, match a second application, "
                      "or establish strict visual V.")
    return report


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("observations", nargs="+", type=Path)
    parser.add_argument("--expect-application", choices=("nyadb", "navicat"), default="")
    parser.add_argument("--output", type=Path, help="Save one JSON report")
    args = parser.parse_args()
    reports = [audit(path, args.expect_application) for path in args.observations]
    result = {"status": ("SIDE_READY" if all(r["status"] == "SIDE_READY" for r in reports)
                         else "BLOCKED"),
              "captures": reports,
              "strict_v_status": "NOT_EVALUATED"}
    payload = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.write_text(payload, encoding="utf-8")
    print(payload, end="")
    raise SystemExit(0 if result["status"] == "SIDE_READY" else 2)


if __name__ == "__main__":
    main()
