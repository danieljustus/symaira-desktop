#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            if cfg!(debug_assertions) {
                app.handle().plugin(
                    tauri_plugin_log::Builder::default()
                        .level(log::LevelFilter::Info)
                        .build(),
                )?;
            }

            let app_handle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                use tauri_plugin_shell::ShellExt;
                use tauri::Emitter;

                let (mut rx, _child) = app_handle
                    .shell()
                    .sidecar("symdesk")
                    .expect("failed to create `symdesk` sidecar")
                    .args(["events", "--json"])
                    .spawn()
                    .expect("failed to spawn sidecar");

                while let Some(event) = rx.recv().await {
                    if let tauri_plugin_shell::process::CommandEvent::Stdout(line_bytes) = event {
                        if let Ok(line) = String::from_utf8(line_bytes) {
                            if let Ok(json) = serde_json::from_str::<serde_json::Value>(&line) {
                                let event_name = json["event"].as_str().unwrap_or("unknown");
                                let _ = app_handle.emit(&format!("core://{}", event_name), json);
                            }
                        }
                    }
                }
            });

            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
