// The shell stays thin: it opens the window, finds the local daemon's token
// and opens links in the browser. Logic belongs in the daemon or the UI.

use tauri::Manager;

/// Reads the token the daemon on this machine wrote on its first run. The
/// UI cannot read files itself, and it needs the token to connect.
#[tauri::command]
fn local_token(app: tauri::AppHandle) -> Result<String, String> {
    let dir = app.path().config_dir().map_err(|e| e.to_string())?;
    let token =
        std::fs::read_to_string(dir.join("worky").join("token")).map_err(|e| e.to_string())?;
    Ok(token.trim().to_string())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .invoke_handler(tauri::generate_handler![local_token])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
