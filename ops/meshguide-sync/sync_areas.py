#!/usr/bin/env python3
"""Sync CoreScope's config.json "areas" from meshguide.dk's community-run
region/city dataset (polygons + hashRegions channel-scope links).

This is dborup/meshview.dk-specific tooling -- meshguide.dk doesn't exist for
other CoreScope deployments, so this lives outside the Go binary/repo core and
is meant to be run manually or via cron/systemd timer, never as part of the
application itself.

Usage:
    sync_areas.py --config /opt/corescope/data/config.json [--dry-run]

What it does:
  1. Fetches https://meshguide.dk/regions.json (polygon per scope) and
     cities.json (scope confirmation + human names).
  2. For areas already in config.json's "areas" that we're confident match a
     meshguide region (see CROSSWALK below -- hand-verified, never guessed),
     sets regionScope and replaces the polygon with meshguide's more precise
     one.
  3. Adds any meshguide region we don't already have as a new area entry, as
     long as it has a real (non-empty) scope assigned.
  4. Anything not in CROSSWALK and not clearly a new region is left alone and
     reported as a warning, never silently linked -- e.g. "dk-sdk" (Syddanmark)
     is NOT the same place as our existing DK_SJ (Sønderjylland) area, so it's
     added as its own new area instead of being merged into DK_SJ.

A timestamped backup of config.json is written before any change.
"""
import argparse
import json
import re
import sys
import urllib.request
from datetime import datetime, timezone

DEFAULT_BASE = "https://meshguide.dk"

# Hand-verified area-key -> meshguide scope mappings. Only pairs we've
# actually confirmed refer to the same place go here.
CROSSWALK = {
    "DK": "dk",
    "JYL": "dk-jylland",
    "DK_NJ": "dk-nj",
    "DK_MJ": "dk-mj",
    "DK_OJ": "dk-oj",
    "DK_3K": "dk-3kant",
    "AAR": "dk-aarhus",
    "AAL": "dk-aalborg",
    "FYN": "dk-fyn",
    "ODE": "dk-fyn-odense",
    "SJL": "dk-sjl",
    "DK_NSJ": "dk-nordsjaelland",
    "DK_LF": "dk-lo-fa",
    "RNN": "dk-bhm",
}

# Areas we deliberately did NOT auto-link, and why -- printed as a reminder
# each run so the mismatch doesn't get silently forgotten.
KNOWN_GAPS = {
    "DK_VJ": "no matching meshguide region found (Vestjylland)",
    "DK_SJ": 'meshguide\'s dk-sdk is "Syddanmark" (a different, broader region than Sønderjylland) -- not linked',
    "CPH": "no matching meshguide region found (Storkøbenhavn)",
    "SE_SKA": 'meshguide\'s se12 is "SydSverige" -- close but not confirmed identical to Skåne -- not linked',
}


def fetch_json(url):
    with urllib.request.urlopen(url, timeout=20) as r:
        return json.load(r)


def normalize_key(scope):
    """dk-fyn-odense -> DK_FYN_ODENSE, se12 -> SE12"""
    return re.sub(r"[^A-Za-z0-9]+", "_", scope).strip("_").upper()


def geojson_ring_to_polygon(geometry):
    """First ring of a GeoJSON Polygon: [lon,lat] -> [lat,lon], closing point dropped."""
    if not geometry or geometry.get("type") != "Polygon":
        return None
    coords = geometry.get("coordinates") or []
    if not coords:
        return None
    ring = coords[0]
    if len(ring) > 1 and ring[0] == ring[-1]:
        ring = ring[:-1]
    return [[round(lat, 6), round(lon, 6)] for lon, lat in ring]


def main():
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--config", required=True, help="Path to CoreScope config.json")
    ap.add_argument("--base-url", default=DEFAULT_BASE)
    ap.add_argument(
        "--dry-run", action="store_true", help="Print what would change, write nothing"
    )
    args = ap.parse_args()

    regions = fetch_json(args.base_url.rstrip("/") + "/regions.json")
    cities = fetch_json(args.base_url.rstrip("/") + "/cities.json")

    # meshguide region keys confirmed to have NO real scope assigned yet
    # (cities.json lists them with scope: "") -- never auto-link or add these.
    no_scope_keys = {k for k, v in cities.items() if not v.get("scope")}

    with open(args.config, "r", encoding="utf-8") as f:
        cfg = json.load(f)
    areas = cfg.setdefault("areas", {})

    changed = []
    warnings = []

    # scopes confirmed real even without a regions.json polygon (e.g. "dk"
    # itself only has a cities.json point, no drawn boundary)
    confirmed_scopes = {v.get("scope") for v in cities.values() if v.get("scope")}
    confirmed_scopes |= set(regions.keys())

    # 1) enrich existing crosswalked areas
    for area_key, scope in CROSSWALK.items():
        entry = areas.get(area_key)
        if entry is None:
            warnings.append(
                f'CROSSWALK references area "{area_key}" which no longer exists in config.json -- skipped'
            )
            continue
        if scope not in confirmed_scopes:
            warnings.append(
                f'CROSSWALK maps {area_key} -> "{scope}" but meshguide no longer has that scope -- skipped'
            )
            continue
        before = json.dumps(entry, sort_keys=True)
        entry["regionScope"] = scope
        polygon = geojson_ring_to_polygon((regions.get(scope) or {}).get("geometry"))
        if polygon:
            entry["polygon"] = polygon
            for k in ("latMin", "latMax", "lonMin", "lonMax"):
                entry.pop(k, None)
        if json.dumps(entry, sort_keys=True) != before:
            changed.append(
                f"enriched {area_key} ({entry.get('label')}) with regionScope={scope}"
                + (" + polygon" if polygon else "")
            )

    # 2) add new areas for meshguide regions we don't have yet
    linked_scopes = set(CROSSWALK.values())
    existing_scopes = {v.get("regionScope") for v in areas.values() if v.get("regionScope")}
    for scope, region in regions.items():
        if scope in no_scope_keys:
            continue
        if scope in linked_scopes or scope in existing_scopes:
            continue
        new_key = normalize_key(scope)
        if new_key in areas:
            continue
        polygon = geojson_ring_to_polygon(region.get("geometry"))
        if not polygon:
            warnings.append(f'meshguide region "{scope}" has no polygon geometry -- skipped')
            continue
        areas[new_key] = {
            "label": region.get("name", scope),
            "polygon": polygon,
            "regionScope": scope,
        }
        changed.append(f"added new area {new_key} ({region.get('name')}) regionScope={scope}")

    for area_key, reason in KNOWN_GAPS.items():
        if area_key in areas and not areas[area_key].get("regionScope"):
            warnings.append(f"{area_key}: {reason}")

    print(f"{len(changed)} change(s):")
    for c in changed:
        print("  -", c)
    if warnings:
        print(f"\n{len(warnings)} warning(s):")
        for w in warnings:
            print("  !", w)

    if not changed:
        print("\nNo changes -- config.json left untouched.")
        return

    if args.dry_run:
        print("\n--dry-run: not writing changes.")
        return

    backup_path = f"{args.config}.bak-{datetime.now(timezone.utc).strftime('%Y%m%d%H%M%S')}"
    with open(args.config, "r", encoding="utf-8") as f:
        raw = f.read()
    with open(backup_path, "w", encoding="utf-8") as f:
        f.write(raw)
    print(f"\nBackup written to {backup_path}")

    with open(args.config, "w", encoding="utf-8") as f:
        json.dump(cfg, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print(f"Wrote changes to {args.config}")
    print("\nRestart corescope for the change to take effect (config is only read at startup).")


if __name__ == "__main__":
    sys.exit(main())
