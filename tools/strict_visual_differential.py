#!/usr/bin/env python3
"""Strict, evidence-gated Navicat/NyaDB 1280x800 visual differential.

It refuses to claim V from similarly named tables or equal screenshot sizes
when the two clients do not have matching DB reads, X11 DPI and UI state.
Requires Pillow. It never connects to or writes to any database.
"""
import argparse
import hashlib
import json
from pathlib import Path

from PIL import Image, ImageChops, ImageStat


_REQUIRED = (
    "database_host_port", "server_identity", "fixture_fingerprint",
    "rows_fingerprint", "schema", "table", "read_status",
    "dpi_x", "dpi_y", "viewport_width", "viewport_height",
    "selected_column", "visible_columns", "sort_rules", "menu_state",
    "screenshot", "observation_evidence",
)
_SAME = (
    "database_host_port", "server_identity", "fixture_fingerprint",
    "rows_fingerprint", "schema", "table", "viewport_width", "viewport_height",
    "selected_column", "visible_columns", "sort_rules", "menu_state",
)


def _resolve(base: Path, name: str) -> Path:
    return (base / name).resolve()


def readiness(manifest: dict, base: Path) -> list[str]:
    errors: list[str] = []
    captures = []
    for side in ("navicat", "nyadb"):
        item = manifest.get(side)
        if not isinstance(item, dict):
            errors.append(f"{side}: missing capture metadata")
            continue
        captures.append((side, item))
        for key in _REQUIRED:
            if key not in item or item[key] is None or item[key] == "":
                errors.append(f"{side}: missing {key}")
        if item.get("read_status") != "success":
            errors.append(f"{side}: live database data read not confirmed")
        if item.get("observation_evidence"):
            if not isinstance(item["observation_evidence"], list) or not item["observation_evidence"]:
                errors.append(f"{side}: observation_evidence must be a nonempty list")
            else:
                for ev in item["observation_evidence"]:
                    if not isinstance(ev, str) or not _resolve(base, ev).is_file():
                        errors.append(f"{side}: evidence file not found: {ev}")
        if item.get("screenshot") and not _resolve(base, item["screenshot"]).is_file():
            errors.append(f"{side}: screenshot missing")
    if len(captures) == 2:
        left = captures[0][1]
        right = captures[1][1]
        for key in _SAME:
            if key in left and key in right and left[key] != right[key]:
                errors.append(f"{key}: reference and candidate differ")
        for key in ("dpi_x", "dpi_y"):
            if isinstance(left.get(key), (int, float)) and isinstance(right.get(key), (int, float)):
                if abs(left[key] - right[key]) > 0.5:
                    errors.append(f"{key}: difference exceeds 0.5 DPI")
        if left.get("rows_fingerprint") == right.get("rows_fingerprint") == "unknown":
            errors.append("matching 'unknown' row hashes are not evidence")
        if left.get("server_identity") == right.get("server_identity") == "unknown":
            errors.append("matching 'unknown' server identities are not evidence")
        if left.get("fixture_fingerprint") == right.get("fixture_fingerprint") == "unknown":
            errors.append("matching 'unknown' fixture fingerprints are not evidence")
    if manifest.get("crop") != [160, 80, 1440, 880]:
        errors.append("expected controlled 1280x800 crop [160,80,1440,880]")
    return errors


def compare(reference: Image.Image, candidate: Image.Image, regions: dict, crop: list[int]) -> dict:
    if reference.size != candidate.size:
        raise ValueError("captured desktop pixel dimensions differ")
    if len(crop) != 4 or crop[2] - crop[0] != 1280 or crop[3] - crop[1] != 800:
        raise ValueError("expected 1280x800 crop")
    if crop[0] < 0 or crop[1] < 0 or crop[2] > reference.width or crop[3] > reference.height:
        raise ValueError("crop extends outside original desktop image")
    a = reference.convert("RGB").crop(tuple(crop))
    b = candidate.convert("RGB").crop(tuple(crop))
    output = {}
    for name, box in regions.items():
        if len(box) != 4 or box[0] < 0 or box[1] < 0 or box[2] > 1280 or box[3] > 800 or box[2] <= box[0] or box[3] <= box[1]:
            raise ValueError(f"invalid ROI {name}: {box}")
        delta = ImageChops.difference(a.crop(tuple(box)), b.crop(tuple(box)))
        mae = sum(ImageStat.Stat(delta).mean) / 3
        pixels = delta.get_flattened_data() if hasattr(delta, "get_flattened_data") else delta.getdata()
        changed = sum(1 for p in pixels if max(p) > 32)
        output[name] = {
            "rgb_mae": round(mae, 3),
            "changed_px_over32_percent": round(100 * changed / (delta.width * delta.height), 3),
        }
    return output


def evaluate(manifest: dict, base: Path) -> dict:
    errors = readiness(manifest, base)
    result = {"status": "BLOCKED", "gate_errors": errors, "regions": {}}
    if errors:
        return result
    a_path = _resolve(base, manifest["navicat"]["screenshot"])
    b_path = _resolve(base, manifest["nyadb"]["screenshot"])
    result["screenshots_sha256"] = {
        "navicat": hashlib.sha256(a_path.read_bytes()).hexdigest(),
        "nyadb": hashlib.sha256(b_path.read_bytes()).hexdigest(),
    }
    try:
        with Image.open(a_path) as a, Image.open(b_path) as b:
            result["regions"] = compare(a, b, manifest["regions"], manifest["crop"])
    except (OSError, ValueError, KeyError) as exc:
        result["gate_errors"].append(f"pixel comparison: {exc}")
        return result
    thresholds = manifest.get("mae_thresholds", {})
    for region, values in result["regions"].items():
        if region not in thresholds:
            result["gate_errors"].append(f"missing acceptance threshold for ROI {region}")
        elif values["rgb_mae"] > thresholds[region]:
            result["gate_errors"].append(
                f"{region}: MAE {values['rgb_mae']} > threshold {thresholds[region]}"
            )
    result["status"] = "PASS" if not result["gate_errors"] else "FAIL"
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--output", type=Path, help="Write result JSON even when blocked")
    args = parser.parse_args()
    manifest = json.loads(args.manifest.read_text(encoding="utf-8"))
    result = evaluate(manifest, args.manifest.resolve().parent)
    payload = json.dumps(result, indent=2, ensure_ascii=False) + "\n"
    if args.output:
        args.output.write_text(payload, encoding="utf-8")
    print(payload, end="")
    raise SystemExit({"PASS": 0, "FAIL": 1, "BLOCKED": 2}[result["status"]])


if __name__ == "__main__":
    main()
