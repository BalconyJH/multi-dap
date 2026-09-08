# Python 2.7. M2 future evidence: per-core source-presence API inventory.
#
# This probe is warm-only and inventory-only. It never sends a debugger command:
# in particular it does not guess an `e` form and never uses breakpoint
# creation/deletion as a source-presence oracle. A callable is not evidence that
# it is read-only; source-presence candidates need documentation that proves both
# properties before a separate, explicitly armed probe can invoke one.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


SOURCE_INVENTORY_TERMS = ("browse", "source", "file", "line", "module")

# MULTI 7.1.6's local script.pdf does not document any public source-file list
# or source-presence API that is both read-only and command-free. In particular,
# PrintFile runs `dbprint f`, while CheckSymbol/GetSymbolAddress/GetSymbolSize
# run debugger expressions. Keep this allowlist empty until a documented API is
# established; inventory discoveries are never callable by themselves.
DOCUMENTED_READ_ONLY_SOURCE_METHODS = ()


def documented_read_only_source_inventory(inventory):
    return dict((name, inventory[name]) for name in DOCUMENTED_READ_ONLY_SOURCE_METHODS
                if name in inventory and inventory[name].get("readable"))


def configured_cores(cfg):
    cores = cfg.get("cores")
    if not isinstance(cores, list) or not cores:
        raise RuntimeError("M2 source-known probe requires configured cores")
    normalized = []
    for core in cores:
        if not isinstance(core, dict):
            raise RuntimeError("M2 source-known core entry must be an object")
        elf = core.get("elf")
        if (not isinstance(elf, basestring) or not elf or elf.startswith("<") or
                "\n" in elf or "\r" in elf):
            raise RuntimeError("M2 source-known core requires an explicit ELF")
        normalized_elf = reconlib._normalized_program_name(elf)
        if not normalized_elf:
            raise RuntimeError("M2 source-known core requires a non-empty ELF")
        normalized.append(normalized_elf)
    if len(set(normalized)) != len(normalized):
        raise RuntimeError("M2 source-known core ELF identities must be unique")
    return cores


rec = reconlib.Recorder("p03_m2_source_known")
sessions = []
try:
    cfg = reconlib.load_config()
    rec.note("config_loaded", True)
    cores = configured_cores(cfg)
    rec.note("configured_core_count", len(cores))

    # Phase one is inventory only.  Every configured core must pass the exact
    # warm identity gate; no primary-core or first-window fallback is allowed.
    inventories = []
    for index, core in enumerate(cores):
        session = rec.step("bind_core_%d_existing_program_window" % index,
                           lambda core=core: reconlib.bind_existing_core_program_window(
                               cfg, core))
        if session is None:
            raise RuntimeError("configured core could not be warm-bound")
        sessions.append(session)
        inventory = rec.step("core_%d_read_only_source_api_inventory" % index,
                             lambda session=session: reconlib.callable_inventory(
                                 session, SOURCE_INVENTORY_TERMS))
        if inventory is None:
            raise RuntimeError("could not inspect source lookup API")
        inventories.append(inventory)

    candidates = [documented_read_only_source_inventory(inventory)
                  for inventory in inventories]
    rec.note("documented_read_only_source_candidate_counts",
             [len(inventory) for inventory in candidates])
    rec.note("source_presence_phase",
             "inventory_only_no_documented_read_only_source_api")
except Exception:
    rec.fatal()
finally:
    for session in sessions:
        reconlib.close_session(session)
    rec.finish()
