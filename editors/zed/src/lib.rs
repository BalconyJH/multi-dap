use serde::{Deserialize, Deserializer};
use zed::serde_json::{self, Value};
use zed_extension_api::{
    self as zed, DebugAdapterBinary, DebugTaskDefinition, StartDebuggingRequestArguments,
    StartDebuggingRequestArgumentsRequest, Worktree,
};

const ADAPTER: &str = "multi-dap";
const MAX_PERF_EVENTS: u32 = 100_000;
const MAX_CONFIG_BYTES: usize = 64 * 1024;

struct MultiDapExtension;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Configuration {
    request: String,
    config_file: String,
    #[serde(default, deserialize_with = "present_string")]
    control_directory: Option<String>,
    #[serde(default, deserialize_with = "present_string")]
    perf_output: Option<String>,
    #[serde(default, deserialize_with = "present_count")]
    perf_max_events: Option<u32>,
}

fn present_string<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<String>, D::Error> {
    String::deserialize(deserializer).map(Some)
}

fn present_count<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<u32>, D::Error> {
    u32::deserialize(deserializer).map(Some)
}

fn request_kind(
    adapter_name: &str,
    config: &Value,
) -> Result<StartDebuggingRequestArgumentsRequest, String> {
    if adapter_name != ADAPTER {
        return Err("multi-dap: unknown debug adapter".into());
    }
    if config.get("request").and_then(Value::as_str) != Some("attach") {
        return Err("multi-dap: request must be attach".into());
    }
    Ok(StartDebuggingRequestArgumentsRequest::Attach)
}

fn windows_absolute_path(value: &str) -> bool {
    if value.trim().is_empty()
        || value.chars().any(char::is_control)
        || value.contains("${")
        || value.contains("$ZED_")
    {
        return false;
    }
    let bytes = value.as_bytes();
    if bytes.len() >= 3
        && bytes[0].is_ascii_alphabetic()
        && bytes[1] == b':'
        && matches!(bytes[2], b'\\' | b'/')
    {
        return true;
    }
    let normalized = value.replace('/', "\\");
    let Some(unc) = normalized.strip_prefix("\\\\") else {
        return false;
    };
    let mut segments = unc.split('\\');
    let valid_segment = |segment: &str| {
        !segment.trim().is_empty()
            && segment != "."
            && segment != ".."
            && !segment.contains([':', '*', '?', '"', '<', '>', '|'])
    };
    matches!(
        (segments.next(), segments.next()),
        (Some(server), Some(share)) if valid_segment(server) && valid_segment(share)
    )
}

fn validate_path(value: &str, name: &str) -> Result<(), String> {
    if !windows_absolute_path(value) {
        return Err(format!(
            "multi-dap: {name} must be a fully qualified Windows path without control characters or task variables"
        ));
    }
    Ok(())
}

fn parse_configuration(
    adapter_name: &str,
    task: &DebugTaskDefinition,
    windows: bool,
) -> Result<Configuration, String> {
    if adapter_name != ADAPTER || task.adapter != ADAPTER {
        return Err("multi-dap: unknown debug adapter".into());
    }
    if !windows {
        return Err("multi-dap: only local Windows debugging is supported".into());
    }
    if task.tcp_connection.is_some() {
        return Err(
            "multi-dap: TCP transport is unsupported; use the authenticated stdio proxy".into(),
        );
    }
    if task.config.len() > MAX_CONFIG_BYTES {
        return Err("multi-dap: debug configuration exceeds its size limit".into());
    }
    let config: Configuration = serde_json::from_str(&task.config)
        .map_err(|_| "multi-dap: invalid or unsupported debug configuration".to_string())?;
    if config.request != "attach" {
        return Err("multi-dap: request must be attach".into());
    }
    validate_path(&config.config_file, "configFile")?;
    if let Some(directory) = &config.control_directory {
        validate_path(directory, "controlDirectory")?;
    }
    if let Some(output) = &config.perf_output {
        validate_path(output, "perfOutput")?;
    }
    if let Some(count) = config.perf_max_events {
        if config.perf_output.is_none() {
            return Err("multi-dap: perfMaxEvents requires perfOutput".into());
        }
        if !(1..=MAX_PERF_EVENTS).contains(&count) {
            return Err("multi-dap: perfMaxEvents must be in 1..100000".into());
        }
    }
    Ok(config)
}

fn adapter_binary(config: Configuration, executable: String) -> Result<DebugAdapterBinary, String> {
    validate_path(&executable, "adapter executable")?;
    let mut arguments = vec!["proxy".into(), "--config".into(), config.config_file];
    if let Some(directory) = config.control_directory {
        arguments.extend(["--control-dir".into(), directory]);
    }
    if let Some(output) = config.perf_output {
        arguments.extend(["--perf-output".into(), output]);
    }
    if let Some(count) = config.perf_max_events {
        arguments.extend(["--perf-max-events".into(), count.to_string()]);
    }
    Ok(DebugAdapterBinary {
        command: Some(executable),
        arguments,
        envs: vec![],
        cwd: None,
        connection: None,
        request_args: StartDebuggingRequestArguments {
            request: StartDebuggingRequestArgumentsRequest::Attach,
            configuration: "{}".into(),
        },
    })
}

impl zed::Extension for MultiDapExtension {
    fn new() -> Self {
        Self
    }

    fn get_dap_binary(
        &mut self,
        adapter_name: String,
        task: DebugTaskDefinition,
        user_provided_debug_adapter_path: Option<String>,
        worktree: &Worktree,
    ) -> Result<DebugAdapterBinary, String> {
        let config = parse_configuration(
            &adapter_name,
            &task,
            matches!(zed::current_platform().0, zed::Os::Windows),
        )?;
        let executable = match user_provided_debug_adapter_path {
            Some(path) => path,
            None => worktree
                .which("multi-dap.exe")
                .ok_or("multi-dap: set dap.multi-dap.binary to the installed executable")?,
        };
        adapter_binary(config, executable)
    }

    fn dap_request_kind(
        &mut self,
        adapter_name: String,
        config: Value,
    ) -> Result<StartDebuggingRequestArgumentsRequest, String> {
        request_kind(&adapter_name, &config)
    }
}

zed::register_extension!(MultiDapExtension);

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn task(config: Value) -> DebugTaskDefinition {
        DebugTaskDefinition {
            label: "Synthetic attach".into(),
            adapter: ADAPTER.into(),
            config: config.to_string(),
            tcp_connection: None,
        }
    }

    fn configuration() -> Value {
        json!({"request": "attach", "configFile": "C:\\private\\project.toml"})
    }

    #[test]
    fn request_kind_only_checks_request_and_adapter() {
        assert!(matches!(
            request_kind(ADAPTER, &json!({"request": "attach", "unknown": true})),
            Ok(StartDebuggingRequestArgumentsRequest::Attach)
        ));
        for request in [Value::Null, json!("launch"), json!(true), json!(1)] {
            assert!(request_kind(ADAPTER, &json!({"request": request})).is_err());
        }
        assert!(request_kind(ADAPTER, &json!({})).is_err());
        assert!(request_kind("other", &json!({"request": "attach"})).is_err());
    }

    #[test]
    fn only_fully_qualified_windows_paths_are_accepted() {
        for valid in [
            r"C:\private\project.toml",
            r"d:/private/project.toml",
            r"\\server\share\project.toml",
            "//server/share/project.toml",
            r"\\server\C$\project.toml",
            r"C:\literal$dollar\project.toml",
            r"C:\space and & shell\project.toml",
        ] {
            assert!(windows_absolute_path(valid), "rejected {valid:?}");
        }
        for invalid in [
            "",
            " ",
            "project.toml",
            r"\project.toml",
            "/project.toml",
            "C:project.toml",
            r"\\server",
            r"\\server\",
            r"\\.\pipe\adapter",
            r"\\?\C:\project.toml",
            r"\\?\UNC\server\share\project.toml",
            r"\\..\share\project.toml",
            r"\\server\..\project.toml",
            r"\\server\?\project.toml",
            r"C:\${workspaceFolder}\project.toml",
            r"C:\$ZED_WORKTREE_ROOT\project.toml",
            "C:\\private\\bad\u{0}name",
            "C:\\private\\bad\nname",
            "C:\\private\\bad\u{7f}name",
            "C:\\private\\bad\u{85}name",
        ] {
            assert!(!windows_absolute_path(invalid), "accepted {invalid:?}");
        }
    }

    #[test]
    fn configuration_rejects_platform_and_tcp_before_executable_resolution() {
        let mut input = task(configuration());
        assert!(parse_configuration(ADAPTER, &input, false).is_err());
        assert!(parse_configuration("other", &input, true).is_err());
        input.adapter = "other".into();
        assert!(parse_configuration(ADAPTER, &input, true).is_err());
        input.adapter = ADAPTER.into();
        input.tcp_connection = Some(zed::TcpArgumentsTemplate {
            host: None,
            port: Some(12345),
            timeout: None,
        });
        assert!(parse_configuration(ADAPTER, &input, true).is_err());
    }

    #[test]
    fn configuration_is_strict_and_errors_never_echo_private_inputs() {
        for field in [
            "host",
            "port",
            "debugServer",
            "binary",
            "args",
            "env",
            "acquisition",
            "primaryElf",
            "build",
            "tcp_connection",
            "adapter",
            "label",
        ] {
            let mut input = configuration();
            input[field] = json!("PRIVATE_PATH");
            let error = parse_configuration(ADAPTER, &task(input), true)
                .err()
                .unwrap();
            assert!(!error.contains("PRIVATE_PATH"));
        }
        for input in [
            r#"{"request":"attach","configFile":"C:\\private\\a","configFile":"C:\\private\\b"}"#,
            r#"{"request":"attach","request":"attach","configFile":"C:\\private\\a"}"#,
            r#"{"request":"attach","configFile":null}"#,
            r#"{"request":"attach","configFile":"C:\\private\\a","controlDirectory":null}"#,
            r#"{"request":"attach","configFile":"C:\\private\\a","perfOutput":null}"#,
            r#"{"request":"attach","configFile":"C:\\private\\a","perfMaxEvents":null}"#,
            r#"{"request":"launch","configFile":"C:\\private\\a"}"#,
            r#"{"configFile":"C:\\private\\a"}"#,
            r#"{"request":"attach","configFile":"C:\\private\\a"} {}"#,
            "[]",
        ] {
            let mut definition = task(configuration());
            definition.config = input.into();
            assert!(parse_configuration(ADAPTER, &definition, true).is_err());
        }
        let mut oversized = task(configuration());
        oversized.config = " ".repeat(MAX_CONFIG_BYTES + 1);
        assert!(parse_configuration(ADAPTER, &oversized, true).is_err());
    }

    #[test]
    fn all_optional_paths_are_validated_without_coercion() {
        for field in ["configFile", "controlDirectory", "perfOutput"] {
            for value in [
                json!("relative"),
                Value::Null,
                json!(2),
                json!("C:\\x\nPRIVATE_PATH"),
            ] {
                let mut input = configuration();
                input[field] = value;
                let error = parse_configuration(ADAPTER, &task(input), true)
                    .err()
                    .unwrap();
                assert!(!error.contains("PRIVATE_PATH"));
            }
        }
    }

    #[test]
    fn proxy_arguments_preserve_paths_and_never_enter_attach_payload() {
        let input = json!({
            "request": "attach",
            "configFile": "C:\\private & literal\\project.toml",
            "controlDirectory": "C:\\private\\control",
            "perfOutput": "C:\\private\\new.trace.json",
            "perfMaxEvents": 23,
        });
        let config = parse_configuration(ADAPTER, &task(input), true).unwrap();
        let binary =
            adapter_binary(config, r"C:\Program Files\multi-dap\multi-dap.exe".into()).unwrap();
        assert_eq!(
            binary.command.as_deref(),
            Some(r"C:\Program Files\multi-dap\multi-dap.exe")
        );
        assert_eq!(
            binary.arguments,
            [
                "proxy",
                "--config",
                r"C:\private & literal\project.toml",
                "--control-dir",
                r"C:\private\control",
                "--perf-output",
                r"C:\private\new.trace.json",
                "--perf-max-events",
                "23",
            ]
        );
        assert!(binary.connection.is_none());
        assert!(binary.cwd.is_none());
        assert!(binary.envs.is_empty());
        assert!(matches!(
            binary.request_args.request,
            StartDebuggingRequestArgumentsRequest::Attach
        ));
        assert_eq!(binary.request_args.configuration, "{}");
    }

    #[test]
    fn default_attach_never_adds_acquisition_or_capture() {
        let config = parse_configuration(ADAPTER, &task(configuration()), true).unwrap();
        let binary = adapter_binary(config, r"C:\tools\multi-dap.exe".into()).unwrap();
        assert_eq!(
            binary.arguments,
            ["proxy", "--config", r"C:\private\project.toml"]
        );
        assert!(binary.connection.is_none());
        assert_eq!(binary.request_args.configuration, "{}");
        for executable in [
            "multi-dap",
            r"C:multi-dap.exe",
            r"\tools\multi-dap.exe",
            "C:\\bad\nPRIVATE_PATH",
        ] {
            let config = parse_configuration(ADAPTER, &task(configuration()), true).unwrap();
            let error = adapter_binary(config, executable.into()).err().unwrap();
            assert!(!error.contains("PRIVATE_PATH"));
        }
    }

    #[test]
    fn capture_limits_are_bounded_and_require_explicit_output() {
        for count in [0, 100_001, u32::MAX] {
            let mut input = configuration();
            input["perfOutput"] = json!("C:\\private\\new.trace.json");
            input["perfMaxEvents"] = json!(count);
            assert!(parse_configuration(ADAPTER, &task(input), true).is_err());
        }
        for invalid in [
            json!(-1),
            json!(1.5),
            json!(true),
            json!("10"),
            Value::Null,
            json!(u64::MAX),
        ] {
            let mut input = configuration();
            input["perfOutput"] = json!("C:\\private\\new.trace.json");
            input["perfMaxEvents"] = invalid;
            assert!(parse_configuration(ADAPTER, &task(input), true).is_err());
        }
        for count in [1, MAX_PERF_EVENTS] {
            let mut input = configuration();
            input["perfMaxEvents"] = json!(count);
            assert!(parse_configuration(ADAPTER, &task(input.clone()), true).is_err());
            input["perfOutput"] = json!("C:\\private\\new.trace.json");
            assert!(parse_configuration(ADAPTER, &task(input), true).is_ok());
        }
    }

    #[test]
    fn registration_and_schema_match_the_runtime_contract() {
        let manifest: toml::Value = toml::from_str(include_str!("../extension.toml")).unwrap();
        assert_eq!(manifest["id"].as_str(), Some(ADAPTER));
        assert_eq!(
            manifest["debug_adapters"][ADAPTER]["schema_path"].as_str(),
            Some("debug_adapter_schemas/multi-dap.json")
        );
        let schema: Value =
            serde_json::from_str(include_str!("../debug_adapter_schemas/multi-dap.json")).unwrap();
        assert_eq!(schema["properties"]["adapter"]["const"], ADAPTER);
        assert_eq!(schema["properties"]["request"]["const"], "attach");
        assert_eq!(schema["additionalProperties"], false);
        assert_eq!(schema["required"], json!(["request", "configFile"]));
        assert_eq!(
            schema["dependencies"]["perfMaxEvents"],
            json!(["perfOutput"])
        );
        assert_eq!(schema["properties"]["perfMaxEvents"]["minimum"], 1);
        assert_eq!(
            schema["properties"]["perfMaxEvents"]["maximum"],
            MAX_PERF_EVENTS
        );
        for property in schema["properties"].as_object().unwrap().values() {
            assert!(property.get("default").is_none());
        }
    }
}
