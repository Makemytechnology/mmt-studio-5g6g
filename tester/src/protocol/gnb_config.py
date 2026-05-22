# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""gNB configuration profiles — JSON CRUD helpers (keyed by gnb_name)."""

import json
import os
import copy


def _load(path):
    if not os.path.exists(path):
        return []
    with open(path, "r") as f:
        return json.load(f)


def _save(path, items):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(items, f, indent=2)


def gnb_cfg_list(path):
    return _load(path)


def gnb_cfg_get(path, gnb_name):
    for item in _load(path):
        if item.get("gnb_name") == gnb_name:
            return item
    return None


def gnb_cfg_add(path, entry):
    items = _load(path)
    name = entry.get("gnb_name", "").strip()
    if not name:
        raise ValueError("gnb_name is required")
    if any(i["gnb_name"] == name for i in items):
        raise ValueError(f"gNB '{name}' already exists")
    items.append(entry)
    _save(path, items)
    return entry


def gnb_cfg_update(path, gnb_name, updates):
    items = _load(path)
    for i, item in enumerate(items):
        if item["gnb_name"] == gnb_name:
            item.update(updates)
            item["gnb_name"] = gnb_name  # don't allow rename via update
            _save(path, items)
            return item
    raise ValueError(f"gNB '{gnb_name}' not found")


def gnb_cfg_delete(path, gnb_name):
    items = _load(path)
    before = len(items)
    items = [i for i in items if i["gnb_name"] != gnb_name]
    if len(items) == before:
        return False
    _save(path, items)
    return True


def gnb_cfg_clone(path, source_name, new_name):
    items = _load(path)
    src = None
    for item in items:
        if item["gnb_name"] == source_name:
            src = item
            break
    if src is None:
        raise ValueError(f"Source gNB '{source_name}' not found")
    if any(i["gnb_name"] == new_name for i in items):
        raise ValueError(f"gNB '{new_name}' already exists")
    new_entry = copy.deepcopy(src)
    new_entry["gnb_name"] = new_name
    items.append(new_entry)
    _save(path, items)
    return new_entry


def gnb_cfg_import(path, new_entries, overwrite=False):
    items = _load(path)
    existing = {i["gnb_name"] for i in items}
    count = 0
    for entry in new_entries:
        name = entry.get("gnb_name", "").strip()
        if not name:
            continue
        if name in existing:
            if overwrite:
                items = [i for i in items if i["gnb_name"] != name]
                items.append(entry)
                count += 1
        else:
            items.append(entry)
            existing.add(name)
            count += 1
    _save(path, items)
    return count
