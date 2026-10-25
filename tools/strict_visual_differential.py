#!/usr/bin/env python3
"""Strict, evidence-gated Navicat/NyaDB visual differential.

It refuses to claim V from similarly named tables or equal screenshot sizes
when the two clients do not have matching DB reads, X11 DPI and UI state.
Requires Pillow. It never connects to or writes to any database.
"""
import argparse
import hashlib
import json
import math
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


def _legacy_readiness(manifest: dict, base: Path) -> list[str]:
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


_SCENES = {"workbench", "connection", "objects", "structure", "data"}
_DB_SCENES = {"objects", "structure", "data"}
_CAPTURE_REQUIRED = (
    "scene", "source_version", "platform", "locale", "theme", "actual_scale",
    "font_family", "font_size_dip", "dpi_x", "dpi_y", "viewport_width",
    "viewport_height", "client_crop", "render_ready", "ui_state", "screenshot",
    "observation_evidence",
)
_CAPTURE_SAME = (
    "scene", "platform", "locale", "theme", "font_family", "font_size_dip",
    "viewport_width", "viewport_height", "ui_state",
)
_DB_COMMON = (
    "database_host_port", "server_identity", "fixture_fingerprint",
    "rows_fingerprint", "schema", "read_status",
)


def _number(value) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def _known(value) -> bool:
    return isinstance(value, str) and value.strip().lower() not in {"", "unknown", "n/a", "none"}


def _box(box, width: int, height: int) -> bool:
    return (isinstance(box, list) and len(box) == 4
            and all(isinstance(n, int) and not isinstance(n, bool) for n in box)
            and 0 <= box[0] < box[2] <= width and 0 <= box[1] < box[3] <= height)


def _evidence_files(item: dict, side: str, base: Path) -> list[str]:
    evidence = item.get("observation_evidence")
    if not isinstance(evidence, list) or not evidence:
        return [f"{side}: observation_evidence must be a nonempty list"]
    return [f"{side}: evidence file not found: {name}" for name in evidence
            if not isinstance(name, str) or not _resolve(base, name).is_file()]


def _capture_binding_errors(item: dict, side: str, base: Path) -> list[str]:
    """Cross-check native capture JSON when it accompanies a V manifest.

    This checks tampering/mix-ups only; a consistent JSON file is still not
    independent evidence that either app connected to the declared database.
    """
    errors = []
    evidence = item.get("observation_evidence")
    if not isinstance(evidence, list):
        return errors
    for name in evidence:
        if not isinstance(name, str) or not name.endswith(".json"):
            continue
        path = _resolve(base, name)
        if not path.is_file():
            continue
        try:
            captured = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, UnicodeError, ValueError):
            errors.append(f"{side}: invalid capture observation JSON: {name}")
            continue
        if not isinstance(captured, dict) or "screenshot_sha256" not in captured:
            continue  # Geometry measurements/other evidence may be JSON too.
        if not isinstance(item.get("screenshot"), str):
            continue
        actual = _resolve(base, item["screenshot"])
        if not actual.is_file():
            continue
        digest = hashlib.sha256(actual.read_bytes()).hexdigest()
        if captured["screenshot_sha256"] != digest:
            errors.append(f"{side}: screenshot digest differs from observation {name}")
        if not isinstance(captured.get("application"), str) or captured["application"].lower() != side:
            errors.append(f"{side}: observation application identity differs: {name}")
        for field in ("scene", "source_version", "platform", "locale", "theme",
                      "viewport_width", "viewport_height", "client_crop"):
            if field in captured and captured[field] != item.get(field):
                errors.append(f"{side}: {field} differs from observation {name}")
        for field in ("actual_scale", "font_family", "font_size_dip", "dpi_x", "dpi_y"):
            if field in captured and captured[field] != item.get(field):
                errors.append(f"{side}: {field} differs from observation {name}")
        if "ui_state" in captured and captured["ui_state"] != item.get("ui_state"):
            errors.append(f"{side}: ui_state differs from observation {name}")
    return errors


def _scene_readiness(manifest: dict, base: Path) -> list[str]:
    errors = []
    scene = manifest.get("scene")
    if not isinstance(scene, str) or scene not in _SCENES:
        return ["scene: expected workbench, connection, objects, structure or data"]
    if manifest.get("manifest_version", 2) != 2:
        errors.append("manifest_version: expected 2 with a scene")
    db_fields = list(_DB_COMMON) if scene in _DB_SCENES else []
    if scene in {"structure", "data"}:
        db_fields.append("table")
    if scene == "data":
        db_fields.extend(("selected_column", "visible_columns", "sort_rules"))
    captures = []
    for side in ("navicat", "nyadb"):
        item = manifest.get(side)
        if not isinstance(item, dict):
            errors.append(f"{side}: missing capture metadata")
            continue
        captures.append(item)
        for key in _CAPTURE_REQUIRED + tuple(db_fields):
            if key not in item or item[key] is None or item[key] == "":
                errors.append(f"{side}: missing {key}")
        for key in ("source_version", "platform", "locale", "theme", "font_family"):
            if not _known(item.get(key)):
                errors.append(f"{side}: {key} must be known")
        if item.get("scene") != scene:
            errors.append(f"{side}: scene differs from manifest")
        if item.get("render_ready") is not True:
            errors.append(f"{side}: rendering is not confirmed ready")
        for key in ("actual_scale", "font_size_dip", "dpi_x", "dpi_y", "viewport_width", "viewport_height"):
            if not _number(item.get(key)) or item[key] <= 0:
                errors.append(f"{side}: {key} must be a positive finite number")
        crop = item.get("client_crop")
        if not _box(crop, 1000000, 1000000):
            errors.append(f"{side}: invalid client_crop")
        elif (crop[2] - crop[0], crop[3] - crop[1]) != (item.get("viewport_width"), item.get("viewport_height")):
            errors.append(f"{side}: client_crop dimensions differ from viewport")
        state = item.get("ui_state")
        if not isinstance(state, dict):
            errors.append(f"{side}: ui_state must describe the captured view")
        else:
            for key in ("database_backed", "loading", "selection", "expanded", "menu_state"):
                if key not in state:
                    errors.append(f"{side}: ui_state missing {key}")
            if state.get("database_backed") is not (scene in _DB_SCENES):
                errors.append(f"{side}: database_backed does not match scene")
            if state.get("loading") is not False:
                errors.append(f"{side}: UI has not finished loading")
            for key in ("selection", "expanded"):
                if not isinstance(state.get(key), list) or not all(isinstance(n, str) for n in state[key]):
                    errors.append(f"{side}: ui_state {key} must be a list of node identities")
            if not _known(state.get("menu_state")):
                errors.append(f"{side}: menu_state must be known")
        if scene in _DB_SCENES:
            if item.get("read_status") != "success":
                errors.append(f"{side}: live database data read not confirmed")
            for key in db_fields:
                if key not in {"visible_columns", "sort_rules"} and not _known(item.get(key)):
                    errors.append(f"{side}: {key} must be known")
            if scene == "data":
                if not isinstance(item.get("visible_columns"), list) or not item["visible_columns"]:
                    errors.append(f"{side}: visible_columns must be a nonempty list")
                if not isinstance(item.get("sort_rules"), list):
                    errors.append(f"{side}: sort_rules must be a list")
        elif any(key in item for key in _DB_COMMON + ("table", "selected_column", "visible_columns", "sort_rules")):
            errors.append(f"{side}: non-database scene cannot contain database capture evidence")
        errors.extend(_evidence_files(item, side, base))
        if not isinstance(item.get("screenshot"), str) or not _resolve(base, item["screenshot"]).is_file():
            errors.append(f"{side}: screenshot missing")
        errors.extend(_capture_binding_errors(item, side, base))
    if len(captures) == 2:
        left, right = captures
        for key in _CAPTURE_SAME + tuple(db_fields):
            if left.get(key) != right.get(key):
                errors.append(f"{key}: reference and candidate differ")
        for key, tolerance in (("dpi_x", 0.5), ("dpi_y", 0.5), ("actual_scale", 0.001)):
            if _number(left.get(key)) and _number(right.get(key)) and abs(left[key] - right[key]) > tolerance:
                errors.append(f"{key}: difference exceeds {tolerance}")
    if not isinstance(manifest.get("regions"), dict) or not manifest["regions"]:
        errors.append("regions: at least one explicit ROI is required")
    for group in ("geometry", "color_samples"):
        if not isinstance(manifest.get(group), dict) or not manifest[group]:
            errors.append(f"{group}: at least one measured landmark is required")
    return errors


def readiness(manifest: dict, base: Path) -> list[str]:
    if not isinstance(manifest, dict):
        return ["manifest must be an object"]
    if "scene" in manifest or "manifest_version" in manifest:
        return _scene_readiness(manifest, base)
    return _legacy_readiness(manifest, base)


def compare(reference: Image.Image, candidate: Image.Image, regions: dict, crop: list[int]) -> dict:
    if reference.size != candidate.size:
        raise ValueError("captured desktop pixel dimensions differ")
    if len(crop) != 4 or crop[2] - crop[0] != 1280 or crop[3] - crop[1] != 800:
        raise ValueError("expected 1280x800 crop")
    if crop[0] < 0 or crop[1] < 0 or crop[2] > reference.width or crop[3] > reference.height:
        raise ValueError("crop extends outside original desktop image")
    a = reference.convert("RGB").crop(tuple(crop))
    b = candidate.convert("RGB").crop(tuple(crop))
    return _compare_regions(a, b, regions)


def _compare_regions(a: Image.Image, b: Image.Image, regions: dict) -> dict:
    output = {}
    for name, box in regions.items():
        if not _box(box, a.width, a.height):
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


def _scene_images(manifest: dict, original: Image.Image, candidate: Image.Image) -> tuple:
    images = []
    for side, image in (("navicat", original), ("nyadb", candidate)):
        crop = manifest[side]["client_crop"]
        if not _box(crop, image.width, image.height):
            raise ValueError(f"{side}: client_crop extends outside screenshot")
        client = image.convert("RGB").crop(tuple(crop))
        histogram = client.convert("L").histogram()
        if sum(histogram[:4]) / (client.width * client.height) >= 0.995:
            raise ValueError(f"{side}: client screenshot is a black frame")
        images.append(client)
    if images[0].size != images[1].size:
        raise ValueError("client pixel dimensions differ")
    return tuple(images)


def _geometry_metrics(manifest: dict, base: Path, size: tuple) -> tuple:
    metrics, failures = {}, []
    for name, sample in manifest["geometry"].items():
        if not isinstance(sample, dict):
            raise ValueError(f"geometry {name}: expected paired measurements")
        rects = []
        for side in ("navicat", "nyadb"):
            rect = sample.get(side)
            if (not isinstance(rect, list) or len(rect) != 4 or not all(_number(n) for n in rect)
                    or rect[0] < 0 or rect[1] < 0 or rect[2] <= 0 or rect[3] <= 0
                    or rect[0] + rect[2] > size[0] or rect[1] + rect[3] > size[1]):
                raise ValueError(f"geometry {name}: invalid {side} [x,y,width,height] in client pixels")
            source = sample.get(side + "_evidence")
            if (not isinstance(source, str) or source not in manifest[side]["observation_evidence"]
                    or not _resolve(base, source).is_file()):
                raise ValueError(f"geometry {name}: {side} measurement source must be capture evidence")
            rects.append([value / manifest[side]["actual_scale"] for value in rect])
        delta = [abs(a - b) for a, b in zip(*rects)]
        metrics[name] = {
            "navicat_dip": rects[0], "nyadb_dip": rects[1],
            "delta_dip": delta, "tolerance_dip": 2,
            "navicat_evidence": sample["navicat_evidence"],
            "nyadb_evidence": sample["nyadb_evidence"],
        }
        if max(delta) > 2:
            failures.append(f"geometry {name}: landmark differs by more than 2 DIP")
    return metrics, failures


def _color_metrics(manifest: dict, a: Image.Image, b: Image.Image) -> tuple:
    metrics, failures = {}, []
    for name, sample in manifest["color_samples"].items():
        if not isinstance(sample, dict):
            raise ValueError(f"color_samples {name}: expected paired client pixel regions")
        colors = []
        for side, image in (("navicat", a), ("nyadb", b)):
            box = sample.get(side)
            if not _box(box, image.width, image.height) or box[2] - box[0] < 2 or box[3] - box[1] < 2:
                raise ValueError(f"color_samples {name}: invalid {side} box (minimum 2x2)")
            region = image.crop(tuple(box))
            if any(high - low > 3 for low, high in region.getextrema()):
                raise ValueError(f"color_samples {name}: {side} sample is not a stable pure-color region")
            colors.append(ImageStat.Stat(region).mean)
        delta = [abs(left - right) for left, right in zip(*colors)]
        metrics[name] = {"navicat_rgb": colors[0], "nyadb_rgb": colors[1],
                         "delta_rgb": delta, "tolerance_per_channel": 3,
                         "navicat_box": sample["navicat"], "nyadb_box": sample["nyadb"]}
        if max(delta) > 3:
            failures.append(f"color {name}: RGB channel difference exceeds 3")
    return metrics, failures


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
            if "scene" in manifest:
                a, b = _scene_images(manifest, a, b)
                geometry, geometry_failures = _geometry_metrics(manifest, base, a.size)
                colors, color_failures = _color_metrics(manifest, a, b)
                regions = _compare_regions(a, b, manifest["regions"])
                result.update(scene=manifest["scene"], regions=regions, geometry=geometry, color_samples=colors)
                result["gate_errors"].extend(geometry_failures + color_failures)
                result["observation_evidence_sha256"] = {
                    side: {name: hashlib.sha256(_resolve(base, name).read_bytes()).hexdigest()
                           for name in manifest[side]["observation_evidence"]}
                    for side in ("navicat", "nyadb")
                }
            else:
                result["regions"] = compare(a, b, manifest["regions"], manifest["crop"])
    except (OSError, ValueError, KeyError, TypeError) as exc:
        result["gate_errors"].append(f"pixel comparison: {exc}")
        return result
    thresholds = manifest.get("mae_thresholds", {})
    if not isinstance(thresholds, dict):
        thresholds = {}
    for region, values in result["regions"].items():
        if region not in thresholds:
            result["gate_errors"].append(f"missing acceptance threshold for ROI {region}")
        elif not _number(thresholds[region]) or not 0 <= thresholds[region] <= 255:
            result["gate_errors"].append(f"invalid acceptance threshold for ROI {region}")
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
