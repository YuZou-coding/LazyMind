import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import test from "node:test";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const mainSource = readFileSync(
  path.join(scriptDir, "..", "electron", "src", "main.js"),
  "utf8",
);

function ipcHandler(channel, nextChannel) {
  const start = mainSource.indexOf(`ipcMain.handle("${channel}"`);
  const end = mainSource.indexOf(`ipcMain.handle("${nextChannel}"`, start + 1);
  assert.ok(start >= 0, `missing ${channel} IPC handler`);
  assert.ok(end > start, `cannot locate end of ${channel} IPC handler`);
  return mainSource.slice(start, end);
}

test("Desktop task workspace selection uses the native directory picker and a five-minute token", () => {
  const handler = ipcHandler(
    "lazymind:selectLocalWorkspace",
    "lazymind:authorizeLocalWorkspace",
  );

  assert.match(handler, /dialog\.showOpenDialog\([\s\S]*properties:\s*\["openDirectory"\]/);
  assert.match(handler, /realpath/);
  assert.match(handler, /5\s*\*\s*60\s*\*\s*1000/);
  assert.match(handler, /selection_token/);
  assert.match(handler, /expires_in_seconds:\s*300/);
  assert.doesNotMatch(handler, /readdir|readFile|createReadStream/);
});

test("Desktop task workspace authorization consumes only the trusted candidate token", () => {
  const handler = ipcHandler(
    "lazymind:authorizeLocalWorkspace",
    "lazymind:selectExecutable",
  );

  assert.match(handler, /selectionToken/);
  assert.match(handler, /selection_token/);
  assert.doesNotMatch(handler, /requestedPaths|body\.path|payload\.path/);
});

test("Desktop task workspace selection does not expand discovery or file-watcher roots", () => {
  const selection = ipcHandler(
    "lazymind:selectLocalWorkspace",
    "lazymind:authorizeLocalWorkspace",
  );
  const authorization = ipcHandler(
    "lazymind:authorizeLocalWorkspace",
    "lazymind:selectExecutable",
  );
  const taskWorkspaceHandlers = selection + authorization;

  assert.doesNotMatch(taskWorkspaceHandlers, /saveAccessState/);
  assert.doesNotMatch(taskWorkspaceHandlers, /replaceFileWatcherAllowedRoots/);
  assert.doesNotMatch(taskWorkspaceHandlers, /allowedRoots/);
  assert.doesNotMatch(taskWorkspaceHandlers, /local-folder-access\.json/);
});
