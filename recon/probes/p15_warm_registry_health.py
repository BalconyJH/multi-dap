# Python 2.7. Non-mutating phase diagnostics for a blocked warm window register.

import os
import sys

sys.path.insert(0, os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


def checkpoint(recorder, stage, ordinal=None, **facts):
    value = {"stage": stage}
    if ordinal is not None:
        value["ordinal"] = ordinal
    value.update(facts)
    recorder.note("checkpoint", value)


def main():
    rec = reconlib.Recorder("p15_warm_registry_health")
    original_get_service = None
    ghs_winreg = None
    try:
        cfg = reconlib.load_config()
        checkpoint(rec, "config_loaded")

        checkpoint(rec, "before_window_register_import")
        import ghs_constants
        import ghs_winreg as imported_ghs_winreg
        ghs_winreg = imported_ghs_winreg
        checkpoint(rec, "after_window_register_import")

        original_get_service = ghs_winreg.GHS_GetGeneralService

        def traced_get_service(*args, **kwargs):
            checkpoint(rec, "before_window_register_ide_open")
            service = original_get_service(*args, **kwargs)
            checkpoint(rec, "after_window_register_ide_open",
                       service_available=bool(service))
            return service

        ghs_winreg.GHS_GetGeneralService = traced_get_service
        checkpoint(rec, "before_window_register_construct")
        registry = ghs_winreg.GHS_WindowRegister()
        checkpoint(rec, "after_window_register_construct",
                   service_available=bool(getattr(registry, "service", None)))
        if not getattr(registry, "service", None):
            raise RuntimeError("window register service is unavailable")

        checkpoint(rec, "before_get_window_list")
        window_list = registry.GetWindowList(False)
        if not isinstance(window_list, dict):
            raise RuntimeError("window register returned an invalid list")
        rows = window_list.get("winInfo")
        if not isinstance(rows, (list, tuple)):
            raise RuntimeError("window register returned invalid rows")
        debugger_class = getattr(ghs_constants.winClassNames, "debugger", "")
        if not debugger_class:
            raise RuntimeError("debugger window class is unavailable")
        debugger_rows = [row for row in rows
                         if isinstance(row, dict) and
                         row.get("className") == debugger_class]
        checkpoint(rec, "after_get_window_list",
                   registered_window_count=len(rows),
                   debugger_window_count=len(debugger_rows))

        windows = []
        for ordinal, row in enumerate(debugger_rows):
            checkpoint(rec, "before_create_debugger_window", ordinal)
            window = registry.CreateWindowObject(
                row["windowName"],
                str(hex(ghs_winreg.ConvertWindowIdFromIDE(row["win"]))),
                row["className"],
                row["serviceName"])
            windows.append(window)
            checkpoint(rec, "after_create_debugger_window", ordinal)

        expected = reconlib._normalized_program_name(cfg.get("primary_elf", ""))
        if not expected:
            raise RuntimeError("primary ELF identity is invalid")
        exact_matches = 0
        stable_matches = 0
        nonempty_info_matches = 0
        for ordinal, window in enumerate(windows):
            checkpoint(rec, "before_get_program", ordinal)
            program = reconlib._normalized_program_name(window.GetProgram())
            is_exact_match = program == expected
            checkpoint(rec, "after_get_program", ordinal,
                       exact_primary_match=is_exact_match)
            if not is_exact_match:
                continue
            exact_matches += 1

            checkpoint(rec, "before_get_status", ordinal)
            status = window.GetStatus()
            is_stable = status in (2, 3) and not isinstance(status, bool)
            checkpoint(rec, "after_get_status", ordinal,
                       stable_status=is_stable)
            if not is_stable:
                continue
            stable_matches += 1

            checkpoint(rec, "before_get_process_info", ordinal)
            info = window.GetCurPrInfo("")
            has_process_info = isinstance(info, dict) and bool(info)
            checkpoint(rec, "after_get_process_info", ordinal,
                       nonempty_process_info=has_process_info)
            if has_process_info:
                nonempty_info_matches += 1

        rec.note("summary", {
            "debugger_window_count": len(windows),
            "exact_primary_matches": exact_matches,
            "stable_matches": stable_matches,
            "nonempty_process_info_matches": nonempty_info_matches,
            "completed": True,
        })
    except Exception:
        rec.fatal()
    finally:
        if ghs_winreg is not None and original_get_service is not None:
            ghs_winreg.GHS_GetGeneralService = original_get_service
        rec.finish()


if __name__ == "__main__":
    main()
