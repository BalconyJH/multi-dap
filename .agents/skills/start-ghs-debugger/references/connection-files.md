# MULTI connection file formats

Read this only when generating or editing `.con` / `.ghsmc`. Skip it when working connection
files already exist.

The download-mode examples below are only for explicitly authorized image preparation,
not observation-only attachment. Check startup actions before launching; these examples
do not grant permission to download, erase, program, reset, or run the target.

## Launch command line

```
multi.exe -socket <port> -connectfile <cfg>.con -connect=<title> <cfg>.ghsmc
```

- `-socket <port>` goes ahead of the other arguments. Omit it and there is no command socket,
  GUI only.
- The value of `-connect=<title>` must match a `target_connection` `title` in the `.con`
  exactly.
- Put the target server's directory on `PATH` before launching MULTI - MULTI resolves
  `command` through `PATH`.

## `.con` - target connection

Tells MULTI which target server to start and with what arguments.

```text
target_connection {
     {
        title = "MY_TARGET_20M"
        type = "Renesas V850/V850E ICE (850eserv2)"
        short_type = "850eserv"
        args = "850eserv2 -df=\"D:\\path\\to\\device.dvf\" -rh850 ..."
        command = "850eserv2 -df=\"D:\\path\\to\\device.dvf\" -rh850 ..."
        logfile = ""
        mode = "download"
        setup_script = ""
        run_mode_partner = ""
        run_mode_policy = ""
        sane = "yes"
        log = "no"
        timestamp = "0"
    }
}
```

Field notes:

- `title` - the name `-connect=` refers to.
- `type` / `short_type` - MULTI's internal connection-type identifiers; they vary by
  architecture and target server. Copy the values already validated for your platform rather
  than inventing them.
- `args` and `command` - the same target server command line, written in both places.
- `mode = "download"` - permits downloading once connected.

Paths inside `args` / `command` must be **Windows style with escaped backslashes**
(`D:\\a\\b.dvf`), and inner double quotes escaped as `\"`. Forward slashes or unescaped
backslashes leave the target server unable to find the device file.

The target server executable and its arguments are architecture-specific (`850eserv2` for
RH850/V850, a different binary elsewhere). Take device file, clock, and probe options from the
platform documentation and treat them as opaque strings unless you have exact grounds to
change them.

## `.ghsmc` - multicore application

Declares which core downloads which ELF. Component numbering and binding follow from it.

```text
version = 1
title = "MY_APP"

core {
  0 { download = {"D:/out/core0.elf"} }
  4 { download = {"D:/out/core1.elf"} }
}
```

- The numbers under `core` are chip core indices, not component numbers.
- `download` paths use forward slashes.
- A single-core session declares one core block.
- In a multicore session, list every image that participates in inter-core communication.
  Downloading only one leaves the other core running whatever was flashed earlier, which
  presents as a communication-protocol defect.

## Generation strategy

Connection files embed machine-local absolute paths, so keep them out of version control.
Regenerate them into the build output directory before each launch, with a session-specific
filename (e.g. `<project>_<side>.con`), so concurrent sessions do not overwrite each other.
