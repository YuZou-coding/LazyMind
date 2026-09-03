import { useEffect, useMemo, useRef, useState } from "react";
import {
  CloseOutlined,
  DeleteOutlined,
  DownOutlined,
  FolderOutlined,
  SearchOutlined,
  SafetyCertificateOutlined,
} from "@ant-design/icons";
import { message } from "antd";
import { useTranslation } from "react-i18next";

import { axiosInstance, localizeErrorCode } from "@/components/request";
import { isDesktopRuntime, isLocalRuntime } from "@/runtime/mode";
import type { WorkspacePermissionMode } from "./types";

export interface LocalWorkspaceView {
  workspace_id: string;
  display_name: string;
  path: string;
  status: "active" | "revoked" | "path_unavailable";
  version?: number;
  read_policy?: "allow";
  write_policy?: "allow";
  affected_task_count?: number;
  permission_mode?: WorkspacePermissionMode;
  permission_version?: number;
}

interface WorkspaceSelectionCandidate {
  canceled: boolean;
  selection_token?: string;
  display_name?: string;
  path?: string;
  expires_in_seconds?: number;
}

interface DesktopWorkspaceBridge {
  selectLocalWorkspace?: () => Promise<WorkspaceSelectionCandidate>;
  reauthorizeLocalWorkspace?: (workspaceId: string) => Promise<WorkspaceSelectionCandidate>;
  authorizeLocalWorkspace?: (selectionToken: string) => Promise<LocalWorkspaceView>;
}

interface Props {
  sessionId?: string;
  disabled?: boolean;
  onSelectedWorkspaceChange: (workspaceId?: string) => void;
  onPermissionModeChange: (mode: WorkspacePermissionMode) => void;
}

function desktopBridge(): DesktopWorkspaceBridge | undefined {
  return (window as Window & { lazymindDesktop?: DesktopWorkspaceBridge }).lazymindDesktop;
}

function coreData<T>(payload: unknown): T {
  const envelope = payload as { data?: T };
  return (envelope?.data ?? payload) as T;
}

async function selectWorkspace(): Promise<WorkspaceSelectionCandidate> {
  if (isDesktopRuntime()) {
    const handler = desktopBridge()?.selectLocalWorkspace;
    if (!handler) throw new Error("LOCAL_WORKSPACE_SELECTION_FORBIDDEN");
    return handler();
  }
  const response = await fetch("/_local/workspaces:select", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw payload;
  return payload as WorkspaceSelectionCandidate;
}

async function authorizeWorkspace(selectionToken: string): Promise<LocalWorkspaceView> {
  if (isDesktopRuntime()) {
    const handler = desktopBridge()?.authorizeLocalWorkspace;
    if (!handler) throw new Error("LOCAL_WORKSPACE_SELECTION_FORBIDDEN");
    return handler(selectionToken);
  }
  const response = await fetch("/_local/workspaces:authorize", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ selection_token: selectionToken }),
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw payload;
  return payload as LocalWorkspaceView;
}

async function prepareWorkspaceReauthorization(workspaceId: string): Promise<WorkspaceSelectionCandidate> {
  if (isDesktopRuntime()) {
    const handler = desktopBridge()?.reauthorizeLocalWorkspace;
    if (!handler) throw new Error("LOCAL_WORKSPACE_SELECTION_FORBIDDEN");
    return handler(workspaceId);
  }
  const response = await fetch("/_local/workspaces:reauthorize", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ workspace_id: workspaceId }),
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw payload;
  return payload as WorkspaceSelectionCandidate;
}

function errorCode(error: unknown): string {
  if (typeof error === "object" && error) {
    const value = error as { code?: string; message?: string };
    return String(value.code || value.message || "");
  }
  return String(error || "");
}

const LOCAL_WORKSPACE_ERROR_CODES: Record<string, string> = {
  LOCAL_WORKSPACE_MODE_FORBIDDEN: "2002321",
  LOCAL_WORKSPACE_SELECTION_FORBIDDEN: "2002322",
  LOCAL_WORKSPACE_SELECTION_EXPIRED: "2002323",
  LOCAL_WORKSPACE_SELECTION_INVALID: "2002324",
  LOCAL_WORKSPACE_PATH_INVALID: "2002325",
  LOCAL_WORKSPACE_NOT_FOUND: "2002326",
  LOCAL_WORKSPACE_REVOKED: "2002327",
  LOCAL_WORKSPACE_BINDING_LOCKED: "2002328",
  LOCAL_WORKSPACE_BINDING_CONFLICT: "2002329",
  LOCAL_WORKSPACE_PATH_UNAVAILABLE: "2002330",
  LOCAL_FILE_ACCESS_NOT_ENABLED: "2002331",
};

function catalogWorkspaceError(error: unknown): string {
  const code = errorCode(error);
  return localizeErrorCode(
    LOCAL_WORKSPACE_ERROR_CODES[code] ?? code,
    localizeErrorCode("2000509"),
  );
}

export default function LocalWorkspaceControl({
  sessionId,
  disabled,
  onSelectedWorkspaceChange,
  onPermissionModeChange,
}: Props) {
  const { t } = useTranslation();
  const existingTask = Boolean(sessionId && !sessionId.startsWith("temp_"));
  const [open, setOpen] = useState(false);
  const [permissionOpen, setPermissionOpen] = useState(false);
  const [allowAllOpen, setAllowAllOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [items, setItems] = useState<LocalWorkspaceView[]>([]);
  const [selected, setSelected] = useState<LocalWorkspaceView | undefined>();
  const [permissionMode, setPermissionMode] = useState<WorkspacePermissionMode>("ask_as_needed");
  const [permissionVersion, setPermissionVersion] = useState(1);
  const [candidate, setCandidate] = useState<WorkspaceSelectionCandidate | undefined>();
  const [authorizationError, setAuthorizationError] = useState("");
  const [revokeError, setRevokeError] = useState("");
  const [revokeOpen, setRevokeOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const controlRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const available = isDesktopRuntime() || isLocalRuntime();

  useEffect(() => {
    if (!available || !existingTask || !sessionId) return;
    onSelectedWorkspaceChange(undefined);
    onPermissionModeChange("ask_as_needed");
    let active = true;
    void fetch(`/api/core/conversations/${encodeURIComponent(sessionId)}:workspace`, {
      credentials: "same-origin",
    })
      .then(async (response) => {
        if (!response.ok) throw new Error("workspace unavailable");
        return response.json();
      })
      .then((payload) => {
        if (!active) return;
        const data = coreData<Record<string, unknown>>(payload);
        const nested = data.workspace as LocalWorkspaceView | undefined;
        const workspace = nested ?? (data as unknown as LocalWorkspaceView);
        if (data.status === "none" || !workspace.workspace_id) {
          setSelected(undefined);
          return;
        }
        const nextMode = (data.permission_mode || workspace.permission_mode || "ask_as_needed") as WorkspacePermissionMode;
        const nextVersion = Number(data.permission_version ?? workspace.permission_version ?? 1);
        setPermissionMode(nextMode);
        setPermissionVersion(nextVersion);
        onPermissionModeChange(nextMode);
        setSelected({
          ...workspace,
          status: String(data.status || workspace.status) as LocalWorkspaceView["status"],
          affected_task_count: Number(data.affected_task_count ?? workspace.affected_task_count ?? 0),
        });
      })
      .catch(() => {
        if (active) setSelected(undefined);
      });
    return () => { active = false; };
  }, [available, existingTask, onPermissionModeChange, onSelectedWorkspaceChange, sessionId]);

  useEffect(() => {
    if (!existingTask && !sessionId) {
      setSelected(undefined);
      setPermissionMode("ask_as_needed");
      onSelectedWorkspaceChange(undefined);
      onPermissionModeChange("ask_as_needed");
    }
  }, [existingTask, onPermissionModeChange, onSelectedWorkspaceChange, sessionId]);

  useEffect(() => {
    if (!open && !permissionOpen && !candidate && !revokeOpen && !allowAllOpen) return;
    if (open) searchRef.current?.focus();
    const closeOnPointerDown = (event: PointerEvent) => {
      if (!controlRef.current?.contains(event.target as Node)) {
        setOpen(false);
        setPermissionOpen(false);
      }
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setOpen(false);
      setPermissionOpen(false);
      setCandidate(undefined);
      setRevokeOpen(false);
      setAllowAllOpen(false);
    };
    document.addEventListener("pointerdown", closeOnPointerDown);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOnPointerDown);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [allowAllOpen, candidate, open, permissionOpen, revokeOpen]);

  const loadRecent = async () => {
    if (existingTask) return;
    try {
      const response = await axiosInstance.get("/api/core/local-workspaces", {
        params: { page_size: 8, include_inactive: true },
        silentError: true,
      } as never);
      const data = coreData<{ items?: LocalWorkspaceView[] }>(response.data);
      setItems(data.items ?? []);
    } catch {
      setItems([]);
    }
  };

  const visibleItems = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    if (!keyword) return items;
    return items.filter((item) => `${item.display_name} ${item.path}`.toLowerCase().includes(keyword));
  }, [items, search]);

  if (!available) return null;

  const chooseNativeFolder = async () => {
    setLoading(true);
    setAuthorizationError("");
    try {
      const next = await selectWorkspace();
      setOpen(false);
      if (!next.canceled) setCandidate(next);
    } catch (error) {
      setAuthorizationError(catalogWorkspaceError(error));
    } finally {
      setLoading(false);
    }
  };

  const allowCandidate = async () => {
    if (!candidate?.selection_token) return;
    setLoading(true);
    setAuthorizationError("");
    try {
      const workspace = await authorizeWorkspace(candidate.selection_token);
      setSelected(workspace);
      setItems((current) => [workspace, ...current.filter((item) => item.workspace_id !== workspace.workspace_id)].slice(0, 8));
      onSelectedWorkspaceChange(workspace.workspace_id);
      setPermissionMode("ask_as_needed");
      onPermissionModeChange("ask_as_needed");
      setCandidate(undefined);
      message.success(t("chat.workspace.authorizedSuccess"));
    } catch (error) {
      setAuthorizationError(catalogWorkspaceError(error));
    } finally {
      setLoading(false);
    }
  };

  const revoke = async () => {
    if (!selected?.workspace_id) return;
    setLoading(true);
    setRevokeError("");
    try {
      const response = await fetch(`/api/core/local-workspaces/${encodeURIComponent(selected.workspace_id)}:revoke`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ version: selected.version ?? 1 }),
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw payload;
      const data = coreData<Partial<LocalWorkspaceView>>(payload);
      if (readonly) {
        setSelected((current) => current ? { ...current, ...data, status: "revoked" } : current);
      } else {
        setSelected(undefined);
      }
      setItems((current) => current.filter((item) => item.workspace_id !== selected.workspace_id));
      onSelectedWorkspaceChange(undefined);
      setPermissionMode("ask_as_needed");
      onPermissionModeChange("ask_as_needed");
      setRevokeOpen(false);
      setOpen(false);
      message.success(t("chat.workspace.revokedSuccess"));
    } catch (error) {
      setRevokeError(catalogWorkspaceError(error));
    } finally {
      setLoading(false);
    }
  };

  const selectRecentWorkspace = async (item: LocalWorkspaceView) => {
    setAuthorizationError("");
    if (item.status === "active") {
      setSelected(item);
      setPermissionMode("ask_as_needed");
      onSelectedWorkspaceChange(item.workspace_id);
      onPermissionModeChange("ask_as_needed");
      setOpen(false);
      message.success(t("chat.workspace.switchedSuccess"));
      return;
    }
    setLoading(true);
    try {
      const next = await prepareWorkspaceReauthorization(item.workspace_id);
      setOpen(false);
      if (!next.canceled) setCandidate(next);
    } catch (error) {
      setAuthorizationError(catalogWorkspaceError(error));
    } finally {
      setLoading(false);
    }
  };

  const applyPermissionMode = async (mode: WorkspacePermissionMode) => {
    if (!selected) return;
    setLoading(true);
    try {
      if (existingTask && sessionId) {
        const response = await axiosInstance.put(
          `/api/core/conversations/${encodeURIComponent(sessionId)}:workspace-permission`,
          { permission_mode: mode, version: permissionVersion },
          { silentError: true } as never,
        );
        const data = coreData<{ permission_version?: number }>(response.data);
        setPermissionVersion(Number(data.permission_version ?? permissionVersion + 1));
      }
      setPermissionMode(mode);
      onPermissionModeChange(mode);
      setPermissionOpen(false);
      setAllowAllOpen(false);
      message.success(t("chat.workspace.permissionUpdated"));
    } catch (error) {
      message.error(catalogWorkspaceError(error));
    } finally {
      setLoading(false);
    }
  };

  const readonly = existingTask;
  const statusSuffix = selected?.status === "revoked"
    ? t("chat.workspace.statusRevoked")
    : selected?.status === "path_unavailable"
      ? t("chat.workspace.statusUnavailable")
      : "";
  const label = selected?.display_name
    ? `${selected.display_name}${statusSuffix ? ` · ${statusSuffix}` : ""}`
    : (existingTask ? t("chat.workspace.none") : t("chat.workspace.select"));

  return (
    <div className="local-workspace-control" ref={controlRef}>
      <button
        type="button"
        className={`input-bottom-actions-left-item local-workspace-trigger${open ? " selected" : ""}`}
        aria-label={label}
        aria-readonly={readonly ? "true" : undefined}
        aria-expanded={open}
        disabled={disabled}
        onClick={() => {
          if (readonly && !selected) return;
          if (!open) setAuthorizationError("");
          setPermissionOpen(false);
          setOpen((current) => !current);
          if (!open) void loadRecent();
        }}
      >
        <FolderOutlined />
        <span>{label}</span>
        {!readonly ? <DownOutlined className="local-workspace-arrow" /> : null}
      </button>

      <button
        type="button"
        className={`input-bottom-actions-left-item local-workspace-permission-trigger${permissionOpen ? " selected" : ""}`}
        aria-label={t(`chat.workspace.permission.${permissionMode}`)}
        aria-expanded={permissionOpen}
        disabled={disabled || !selected || selected.status !== "active"}
        onClick={() => {
          setOpen(false);
          setPermissionOpen((current) => !current);
        }}
      >
        <SafetyCertificateOutlined />
        <span>{t(`chat.workspace.permission.${permissionMode}`)}</span>
        <DownOutlined className="local-workspace-arrow" />
      </button>

      {permissionOpen ? (
        <div className="local-workspace-permission-menu" role="menu" aria-label={t("chat.workspace.permission.menuLabel")}>
          {(["always_ask", "ask_as_needed", "allow_all"] as WorkspacePermissionMode[]).map((mode) => (
            <button
              key={mode}
              type="button"
              role="menuitem"
              disabled={loading}
              onClick={() => {
                if (mode === "allow_all") {
                  setPermissionOpen(false);
                  setAllowAllOpen(true);
                } else {
                  void applyPermissionMode(mode);
                }
              }}
            >
              <strong>{t(`chat.workspace.permission.${mode}`)}</strong>
              <small>{t(`chat.workspace.permission.${mode}Description`)}</small>
            </button>
          ))}
        </div>
      ) : null}

      {open ? (
        <div className="local-workspace-menu" role="menu">
          {!readonly ? (
            <label className="local-workspace-search">
              <SearchOutlined />
              <input
                ref={searchRef}
                type="search"
                aria-label={t("chat.workspace.search")}
                placeholder={t("chat.workspace.search")}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </label>
          ) : null}
          {!readonly && authorizationError ? (
            <p className="local-workspace-menu-error" role="alert">{authorizationError}</p>
          ) : null}
          {!readonly ? visibleItems.map((item) => (
            <div className="local-workspace-row" key={item.workspace_id}>
              <button
                type="button"
                className="local-workspace-row-select"
                onClick={() => void selectRecentWorkspace(item)}
              >
                <FolderOutlined />
                <span><strong>{item.display_name}</strong><small title={item.path}>{item.path}</small></span>
              </button>
              <button
                type="button"
                className="local-workspace-row-revoke"
                aria-label={`${t("chat.workspace.revoke")} ${item.display_name}`}
                onClick={() => {
                  setSelected(item);
                  setOpen(false);
                  setRevokeError("");
                  setRevokeOpen(true);
                }}
              >
                <DeleteOutlined />
              </button>
            </div>
          )) : null}
          {readonly && selected ? (
            <div className="local-workspace-readonly-detail">
              <strong>{selected.display_name}</strong>
              <span title={selected.path}>{selected.path}</span>
              {selected.status === "active" ? (
                <button
                  type="button"
                  aria-label={t("chat.workspace.revoke")}
                  onClick={() => {
                    setOpen(false);
                    setRevokeError("");
                    setRevokeOpen(true);
                  }}
                >
                  <DeleteOutlined /> {t("chat.workspace.revoke")}
                </button>
              ) : null}
            </div>
          ) : null}
          {!readonly ? <div className="local-workspace-divider" /> : null}
          {!readonly ? (
            <button
              type="button"
              className="local-workspace-action"
              aria-label={t("chat.workspace.openLocal")}
              disabled={loading}
              onClick={() => void chooseNativeFolder()}
            >
              <FolderOutlined /> {t("chat.workspace.openLocal")}
            </button>
          ) : null}
          {!readonly ? (
            <button
              type="button"
              className="local-workspace-action"
              aria-label={t("chat.workspace.none")}
              onClick={() => {
                setSelected(undefined);
                onSelectedWorkspaceChange(undefined);
                setPermissionMode("ask_as_needed");
                onPermissionModeChange("ask_as_needed");
                setOpen(false);
                message.success(t("chat.workspace.clearedSuccess"));
              }}
            >
              <CloseOutlined /> {t("chat.workspace.none")}
            </button>
          ) : null}
        </div>
      ) : null}

      {candidate ? (
        <div className="local-workspace-modal-backdrop" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setCandidate(undefined);
        }}>
          <section className="local-workspace-modal" role="dialog" aria-modal="true" aria-label={t("chat.workspace.authorizationTitle")}>
            <button type="button" className="local-workspace-modal-close" aria-label={t("common.close")} onClick={() => setCandidate(undefined)}>
              <CloseOutlined />
            </button>
            <h2>{t("chat.workspace.authorizationTitle")}</h2>
            <p className="local-workspace-modal-question">{t("chat.workspace.authorizationQuestion", { name: candidate.display_name })}</p>
            <div className="local-workspace-folder-card">
              <FolderOutlined />
              <span><strong>{candidate.display_name}</strong><small title={candidate.path}>{candidate.path}</small></span>
            </div>
            <p className="local-workspace-modal-scope">{t("chat.workspace.authorizationScope")}</p>
            {authorizationError ? <p className="local-workspace-error" role="alert">{authorizationError}</p> : null}
            <footer>
              <button type="button" onClick={() => setCandidate(undefined)}>{t("chat.workspace.cancel")}</button>
              <button type="button" className="primary" disabled={loading} onClick={() => void allowCandidate()}>{t("chat.workspace.allow")}</button>
            </footer>
          </section>
        </div>
      ) : null}

      {revokeOpen && selected ? (
        <div className="local-workspace-modal-backdrop" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setRevokeOpen(false);
        }}>
          <section className="local-workspace-modal local-workspace-revoke-modal" role="dialog" aria-modal="true" aria-label={t("chat.workspace.revokeTitle")}>
            <h2>{t("chat.workspace.revokeTitle")}</h2>
            <p>{selected.path}</p>
            <p>
              {t("chat.workspace.revokeAffected")} <strong>{selected.affected_task_count ?? 0}</strong>
            </p>
            {revokeError ? <p className="local-workspace-error" role="alert">{revokeError}</p> : null}
            <footer>
              <button type="button" onClick={() => setRevokeOpen(false)}>{t("chat.workspace.cancel")}</button>
              <button type="button" className="danger" disabled={loading} onClick={() => void revoke()}>{t("chat.workspace.revoke")}</button>
            </footer>
          </section>
        </div>
      ) : null}

      {allowAllOpen ? (
        <div className="local-workspace-modal-backdrop" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setAllowAllOpen(false);
        }}>
          <section className="local-workspace-modal local-workspace-risk-modal" role="dialog" aria-modal="true" aria-label={t("chat.workspace.permission.allowAllTitle")}>
            <button type="button" className="local-workspace-modal-close" aria-label={t("common.close")} onClick={() => setAllowAllOpen(false)}>
              <CloseOutlined />
            </button>
            <h2>{t("chat.workspace.permission.allowAllTitle")}</h2>
            <p>{t("chat.workspace.permission.allowAllIntro")}</p>
            <ul>
              <li><strong>{t("chat.workspace.permission.fileRisk")}</strong><span>{t("chat.workspace.permission.fileRiskDescription")}</span></li>
              <li><strong>{t("chat.workspace.permission.commandRisk")}</strong><span>{t("chat.workspace.permission.commandRiskDescription")}</span></li>
              <li><strong>{t("chat.workspace.permission.networkRisk")}</strong><span>{t("chat.workspace.permission.networkRiskDescription")}</span></li>
              <li><strong>{t("chat.workspace.permission.connectedAppRisk")}</strong><span>{t("chat.workspace.permission.connectedAppRiskDescription")}</span></li>
            </ul>
            <footer>
              <button type="button" onClick={() => setAllowAllOpen(false)}>{t("chat.workspace.cancel")}</button>
              <button type="button" className="primary" disabled={loading} onClick={() => void applyPermissionMode("allow_all")}>{t("chat.workspace.permission.confirmAllowAll")}</button>
            </footer>
          </section>
        </div>
      ) : null}
    </div>
  );
}
