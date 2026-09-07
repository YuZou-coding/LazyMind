import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { message } from "antd";
import { forwardRef, useImperativeHandle } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { axiosInstance } from "@/components/request";
import ChatInput from "./index";

const mocks = vi.hoisted(() => ({
  selectLocalWorkspace: vi.fn(),
  reauthorizeLocalWorkspace: vi.fn(),
  authorizeLocalWorkspace: vi.fn(),
  onSend: vi.fn(),
  setThinkingDepth: vi.fn(),
  desktopRuntime: true,
  localRuntime: false,
}));

vi.mock("antd", async (importOriginal) => {
  const actual = await importOriginal<typeof import("antd")>();
  return {
    ...actual,
    message: {
      success: vi.fn(),
      error: vi.fn(),
      destroy: vi.fn(),
    },
  };
});

vi.mock("@/runtime/mode", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/runtime/mode")>()),
  isDesktopRuntime: () => mocks.desktopRuntime,
  isLocalRuntime: () => mocks.localRuntime,
}));

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: () => undefined },
  useTranslation: () => ({
    t: (key: string) => ({
      "chat.addResource": "添加",
      "chat.addResourceTooltip": "添加",
      "chat.inputPlaceholder": "请输入您的问题",
      "chat.thinkingDepth": "思考深度",
      "chat.thinkingDepthMedium": "思考深度：中",
      "chat.send": "发送",
      "chat.stopGenerate": "停止生成",
      "chat.conversationConfig": "对话配置",
      "chat.workspace.select": "选择工作区",
      "chat.workspace.search": "搜索工作区",
      "chat.workspace.openLocal": "打开本地文件夹",
      "chat.workspace.none": "不使用本地工作区",
      "chat.workspace.authorizationTitle": "首次使用时需要授权",
      "chat.workspace.authorizationQuestion": "允许 LazyMind 访问“Documents”工作区？",
      "chat.workspace.authorizationScope": "授权仅适用于该文件夹及其子目录，默认在修改文件前询问。",
      "chat.workspace.cancel": "取消",
      "chat.workspace.allow": "允许访问",
      "chat.workspace.confirmationMode": "按需确认",
      "chat.workspace.permissionAlwaysAsk": "始终询问",
      "chat.workspace.permissionAskAsNeeded": "按需确认",
      "chat.workspace.permissionAllowAll": "全部允许",
      "chat.workspace.allowAllTitle": "开启全部允许？",
      "chat.workspace.allowAllFilesRisk": "文件风险",
      "chat.workspace.allowAllCommandsRisk": "命令风险",
      "chat.workspace.allowAllNetworkRisk": "联网风险",
      "chat.workspace.allowAllAppsRisk": "已连接应用风险",
      "chat.workspace.confirmAllowAll": "确认开启",
      "chat.workspace.workspaceChanged": "工作区已切换",
      "chat.workspace.workspaceAuthorized": "工作区已授权",
      "chat.workspace.permissionChanged": "权限已修改",
      "chat.workspace.permission.menuLabel": "权限模式",
      "chat.workspace.permission.always_ask": "始终询问",
      "chat.workspace.permission.always_askDescription": "文件编辑等操作均需确认",
      "chat.workspace.permission.ask_as_needed": "按需确认",
      "chat.workspace.permission.ask_as_neededDescription": "仅在风险操作前询问",
      "chat.workspace.permission.allow_all": "全部允许",
      "chat.workspace.permission.allow_allDescription": "自动执行可批准操作",
      "chat.workspace.permission.allowAllTitle": "要开启“全部允许”吗？",
      "chat.workspace.permission.allowAllIntro": "风险说明",
      "chat.workspace.permission.fileRisk": "文件风险",
      "chat.workspace.permission.fileRiskDescription": "文件风险说明",
      "chat.workspace.permission.commandRisk": "命令风险",
      "chat.workspace.permission.commandRiskDescription": "命令风险说明",
      "chat.workspace.permission.networkRisk": "联网风险",
      "chat.workspace.permission.networkRiskDescription": "联网风险说明",
      "chat.workspace.permission.connectedAppRisk": "已连接应用风险",
      "chat.workspace.permission.connectedAppRiskDescription": "应用风险说明",
      "chat.workspace.permission.networkAndConnectedAppsRisk": "互联网和已连接应用风险",
      "chat.workspace.permission.networkAndConnectedAppsRiskDescription": "联网和应用风险说明",
      "chat.workspace.permission.allowAllWarning": "风险警告",
      "chat.workspace.permission.confirmAllowAll": "确认开启",
      "chat.workspace.revoke": "撤销授权",
      "chat.workspace.revokeTitle": "撤销工作区授权？",
      "chat.workspace.selectionExpired": "文件夹选择已过期，请重新选择。",
      "chat.workspace.authorizedSuccess": "工作区已授权",
      "chat.workspace.switchedSuccess": "工作区已切换",
      "chat.workspace.clearedSuccess": "已停用本地工作区",
      "chat.workspace.permissionUpdated": "权限已修改",
      "common.close": "关闭",
    }[key] ?? key),
  }),
}));

vi.mock("@/modules/memory/toolApi", () => ({
  listToolAssetsPage: vi.fn().mockResolvedValue({ records: [] }),
  TOOL_AVAILABILITY_CHANGED_EVENT: "lazymind:tool-availability-changed",
}));

vi.mock("@/modules/chat/store/chatThink", () => ({
  THINKING_DEPTH_VALUES: ["medium"],
  THINKING_DEPTH_LABEL_KEYS: { medium: "chat.thinkingDepthMedium" },
  useChatThinkStore: () => ({
    thinkingDepth: "medium",
    setThinkingDepth: mocks.setThinkingDepth,
  }),
}));

vi.mock("@/modules/chat/store/chatNewMessage", () => ({
  useChatNewMessageStore: () => ({ setNewMessage: vi.fn() }),
}));

vi.mock("@/modules/chat/store/chatMessage", () => ({
  useChatMessageStore: () => ({
    setPendingMessage: vi.fn(),
    clearPendingMessage: vi.fn(),
  }),
}));

vi.mock("@/modules/chat/store/chatInput", () => ({
  useChatInputStore: () => ({
    saveInputContent: vi.fn(),
    getInputContent: vi.fn().mockReturnValue(""),
    clearInputContent: vi.fn(),
  }),
}));

vi.mock("./MentionEditor", () => ({
  default: forwardRef(function MentionEditorMock(props: any, ref) {
    useImperativeHandle(ref, () => ({ focus: vi.fn() }));
    return (
      <textarea
        aria-label="message-input"
        value={props.value}
        onChange={(event) => props.onChange(event.target.value)}
      />
    );
  }),
}));

vi.mock("../ImageUpload", () => ({
  allowedUploadTypes: [],
  default: forwardRef(function ImageUploadMock(_props: any, ref) {
    useImperativeHandle(ref, () => ({
      getFiles: () => [],
      openFileDialog: vi.fn(),
      uploadFiles: vi.fn(),
    }));
    return null;
  }),
}));

vi.mock("../ChatSelector", () => ({
  default: forwardRef(function ChatSelectorMock(_props: any, ref) {
    useImperativeHandle(ref, () => ({ open: vi.fn() }));
    return null;
  }),
}));

vi.mock("../PromptModal", () => ({
  default: forwardRef(function PromptModalMock(_props: any, ref) {
    useImperativeHandle(ref, () => ({ onOpen: vi.fn() }));
    return null;
  }),
}));

vi.mock("../BatchChat", () => ({
  default: forwardRef(function BatchChatMock(_props: any, ref) {
    useImperativeHandle(ref, () => ({}));
    return null;
  }),
}));

vi.mock("../ShowChatFileList", () => ({ default: () => null }));
vi.mock("./ContextUsageButton", () => ({ default: () => null }));
vi.mock("./ChatConfigModal", () => ({
  default: () => <button type="button">对话配置</button>,
}));

function renderComposer(runInBackground: boolean, value = "", sessionId?: string) {
  return render(
    <ChatInput
      value={value}
      onChange={vi.fn()}
      onSend={mocks.onSend}
      isChatContent
      showHistoryList={false}
      showHistoryButton={false}
      showPromptSuggestions={false}
      showSkillDeposit={false}
      runInBackground={runInBackground}
      sessionId={sessionId}
    />,
  );
}

describe("Local/Desktop task workspace composer contract", () => {
  beforeEach(() => {
    mocks.selectLocalWorkspace.mockReset();
    mocks.reauthorizeLocalWorkspace.mockReset();
    mocks.authorizeLocalWorkspace.mockReset();
    mocks.onSend.mockReset();
    mocks.desktopRuntime = true;
    mocks.localRuntime = false;
    Object.defineProperty(window, "lazymindDesktop", {
      configurable: true,
      value: {
        selectLocalWorkspace: mocks.selectLocalWorkspace,
        reauthorizeLocalWorkspace: mocks.reauthorizeLocalWorkspace,
        authorizeLocalWorkspace: mocks.authorizeLocalWorkspace,
      },
    });
  });

  afterEach(() => {
    message.destroy();
    vi.restoreAllMocks();
    Reflect.deleteProperty(window, "lazymindDesktop");
  });

  it("places workspace selection after Add only for New task mode", () => {
    const { container, rerender } = renderComposer(true);
    expect(screen.getByRole("button", { name: "选择工作区" })).toBeInTheDocument();

    const toolbar = container.querySelector(".input-bottom-actions-left");
    expect(toolbar).not.toBeNull();
    const copy = toolbar?.textContent ?? "";
    expect(copy.indexOf("添加")).toBeLessThan(copy.indexOf("选择工作区"));
    expect(copy.indexOf("选择工作区")).toBeLessThan(copy.indexOf("按需确认"));
    expect(copy.indexOf("按需确认")).toBeLessThan(copy.indexOf("思考深度：中"));

    rerender(
      <ChatInput
        value=""
        onChange={vi.fn()}
        onSend={mocks.onSend}
        isChatContent
        showHistoryList={false}
        showHistoryButton={false}
        showPromptSuggestions={false}
        showSkillDeposit={false}
        runInBackground={false}
      />,
    );
    expect(screen.queryByRole("button", { name: "选择工作区" })).not.toBeInTheDocument();
  });

  it("hides the workspace entry when neither Local nor Desktop is active", () => {
    mocks.desktopRuntime = false;
    mocks.localRuntime = false;

    renderComposer(true);

    expect(screen.queryByRole("button", { name: "选择工作区" })).not.toBeInTheDocument();
  });

  it("shows the required menu structure without scanning the disk", async () => {
    renderComposer(true);
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));

    expect(await screen.findByRole("searchbox", { name: "搜索工作区" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "打开本地文件夹" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "不使用本地工作区" })).toBeInTheDocument();
    expect(mocks.selectLocalWorkspace).not.toHaveBeenCalled();
  });

  it("toggles the workspace menu and filters recent workspaces by name or path", async () => {
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [
        { workspace_id: "one", display_name: "Alpha", path: "/projects/finance", status: "active" },
        { workspace_id: "two", display_name: "Beta", path: "/projects/research", status: "active" },
      ] } },
    } as never);
    renderComposer(true);
    const trigger = screen.getByRole("button", { name: "选择工作区" });

    fireEvent.click(trigger);
    const search = await screen.findByRole("searchbox", { name: "搜索工作区" });
    expect(screen.getByRole("button", { name: /Alpha/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Beta/ })).toBeInTheDocument();
    fireEvent.change(search, { target: { value: "finance" } });
    expect(screen.getByRole("button", { name: /Alpha/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Beta/ })).not.toBeInTheDocument();
    fireEvent.change(search, { target: { value: "Beta" } });
    expect(screen.queryByRole("button", { name: /Alpha/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Beta/ })).toBeInTheDocument();
    fireEvent.click(trigger);
    expect(screen.queryByRole("searchbox", { name: "搜索工作区" })).not.toBeInTheDocument();
  });

  it("requires the LazyMind authorization modal after the native macOS selection", async () => {
    mocks.selectLocalWorkspace.mockResolvedValue({
      canceled: false,
      selection_token: "selection-token",
      display_name: "Documents",
      path: "/Users/alice/Documents",
      expires_in_seconds: 300,
    });
    mocks.authorizeLocalWorkspace.mockResolvedValue({
      workspace_id: "workspace-1",
      display_name: "Documents",
      path: "/Users/alice/Documents",
      status: "active",
      read_policy: "allow",
      write_policy: "ask_before_write",
    });
    renderComposer(true);

    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: "打开本地文件夹" }));

    expect(mocks.selectLocalWorkspace).toHaveBeenCalledOnce();
    expect(await screen.findByRole("dialog", { name: "首次使用时需要授权" })).toBeInTheDocument();
    expect(screen.getByText("允许 LazyMind 访问“Documents”工作区？")).toBeInTheDocument();
    expect(screen.getByText("/Users/alice/Documents")).toBeInTheDocument();
    expect(screen.getByText("授权仅适用于该文件夹及其子目录，默认在修改文件前询问。")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(mocks.authorizeLocalWorkspace).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "首次使用时需要授权" })).not.toBeInTheDocument();
  });

  it("keeps the pending selection unchanged when the native picker is canceled", async () => {
    mocks.selectLocalWorkspace.mockResolvedValue({ canceled: true });
    renderComposer(true);

    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: "打开本地文件夹" }));

    await waitFor(() => expect(mocks.selectLocalWorkspace).toHaveBeenCalledOnce());
    expect(screen.queryByRole("dialog", { name: "首次使用时需要授权" })).not.toBeInTheDocument();
    expect(mocks.authorizeLocalWorkspace).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "选择工作区" })).toBeInTheDocument();
  });

  it("closes authorization by close button or backdrop without authorizing", async () => {
    mocks.selectLocalWorkspace.mockResolvedValue({
      canceled: false,
      selection_token: "selection-token",
      display_name: "Documents",
      path: "/Users/alice/Documents",
    });
    const { container } = renderComposer(true);
    const openCandidate = async () => {
      fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
      fireEvent.click(await screen.findByRole("button", { name: "打开本地文件夹" }));
      return screen.findByRole("dialog", { name: "首次使用时需要授权" });
    };

    await openCandidate();
    fireEvent.click(screen.getByRole("button", { name: "关闭" }));
    expect(screen.queryByRole("dialog", { name: "首次使用时需要授权" })).not.toBeInTheDocument();
    await openCandidate();
    const backdrop = container.querySelector(".local-workspace-modal-backdrop");
    expect(backdrop).not.toBeNull();
    fireEvent.mouseDown(backdrop as Element);
    expect(screen.queryByRole("dialog", { name: "首次使用时需要授权" })).not.toBeInTheDocument();
    expect(mocks.authorizeLocalWorkspace).not.toHaveBeenCalled();
  });

  it("authorizes by one-time token and sends only workspace_id with the first task", async () => {
    mocks.selectLocalWorkspace.mockResolvedValue({
      canceled: false,
      selection_token: "selection-token",
      display_name: "Documents",
      path: "/Users/alice/Documents",
      expires_in_seconds: 300,
    });
    mocks.authorizeLocalWorkspace.mockResolvedValue({
      workspace_id: "workspace-1",
      display_name: "Documents",
      path: "/Users/alice/Documents",
      status: "active",
      read_policy: "allow",
      write_policy: "ask_before_write",
    });
    renderComposer(true, "整理文档");

    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: "打开本地文件夹" }));
    fireEvent.click(await screen.findByRole("button", { name: "允许访问" }));

    await waitFor(() => {
      expect(mocks.authorizeLocalWorkspace).toHaveBeenCalledWith("selection-token");
    });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));

    expect(mocks.onSend).toHaveBeenCalledWith(expect.objectContaining({
      run_in_background: true,
      workspace_id: "workspace-1",
    }));
    expect(mocks.onSend.mock.calls[0][0]).not.toHaveProperty("path");
    expect(mocks.onSend.mock.calls[0][0]).not.toHaveProperty("selection_token");
  });

  it("does not bind or clear the prompt when the selection token expires", async () => {
    mocks.selectLocalWorkspace.mockResolvedValue({
      canceled: false,
      selection_token: "expired-token",
      display_name: "Documents",
      path: "/Users/alice/Documents",
      expires_in_seconds: 300,
    });
    mocks.authorizeLocalWorkspace.mockRejectedValue({
      code: "LOCAL_WORKSPACE_SELECTION_EXPIRED",
    });
    renderComposer(true, "保留这段输入");

    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: "打开本地文件夹" }));
    fireEvent.click(await screen.findByRole("button", { name: "允许访问" }));

    expect(await screen.findByText("文件夹选择已过期，请重新选择。")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "message-input" })).toHaveValue("保留这段输入");
    expect(mocks.onSend).not.toHaveBeenCalled();
  });

  it("uses the loopback Local host flow without accepting a browser path", async () => {
    mocks.desktopRuntime = false;
    mocks.localRuntime = true;
    Reflect.deleteProperty(window, "lazymindDesktop");
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(new Response(JSON.stringify({
        canceled: false,
        selection_token: "local-selection-token",
        display_name: "Documents",
        path: "/Users/alice/Documents",
        expires_in_seconds: 300,
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        workspace_id: "workspace-local-1",
        display_name: "Documents",
        path: "/Users/alice/Documents",
        status: "active",
        read_policy: "allow",
        write_policy: "ask_before_write",
      }), { status: 200, headers: { "Content-Type": "application/json" } }));

    renderComposer(true);
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: "打开本地文件夹" }));

    await waitFor(() => expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "/_local/workspaces:select",
      expect.objectContaining({ method: "POST", credentials: "same-origin" }),
    ));
    fireEvent.click(await screen.findByRole("button", { name: "允许访问" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/_local/workspaces:authorize");
    const authorizeInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(JSON.parse(String(authorizeInit.body))).toEqual({
      selection_token: "local-selection-token",
    });
  });

  it("keeps an existing Work task read-only and revokes by workspace id after confirmation", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/api/core/conversations/task-existing:workspace") {
        return new Response(JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          workspace_id: "workspace-1",
          display_name: "Documents",
          path: "/Users/alice/Documents",
          status: "active",
          affected_task_count: 2,
        },
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (url === "/api/core/local-workspaces/workspace-1:revoke") {
        return new Response(JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          workspace_id: "workspace-1",
          status: "revoked",
          affected_task_count: 2,
        },
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      throw new Error(`unexpected request: ${url}`);
    });

    renderComposer(true, "继续整理", "task-existing");

    const workspaceButton = await screen.findByRole("button", { name: "Documents" });
    expect(workspaceButton).toHaveAttribute("aria-readonly", "true");
    fireEvent.click(workspaceButton);
    fireEvent.click(await screen.findByRole("button", { name: "撤销授权" }));

    expect(await screen.findByRole("dialog", { name: "撤销工作区授权？" })).toBeInTheDocument();
    expect(screen.getByText("/Users/alice/Documents")).toBeInTheDocument();
    expect(screen.getByText(/2/)).toBeInTheDocument();
    const confirmations = screen.getAllByRole("button", { name: "撤销授权" });
    fireEvent.click(confirmations[confirmations.length - 1]);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/api/core/local-workspaces/workspace-1:revoke");
    expect(String(fetchMock.mock.calls[1][1]?.body ?? "")).not.toContain("/Users/alice/Documents");
  });

  it("does not let an existing Work task without a workspace bind one later", async () => {
    let resolveWorkspaceLookup!: (response: Response) => void;
    const workspaceLookup = new Promise<Response>((resolve) => {
      resolveWorkspaceLookup = resolve;
    });
    vi.spyOn(globalThis, "fetch").mockReturnValue(workspaceLookup);

    renderComposer(true, "继续任务", "task-without-workspace");

    await act(async () => {
      resolveWorkspaceLookup(new Response(JSON.stringify({
        code: 0,
        message: "ok",
        data: { status: "none" },
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
      await workspaceLookup;
    });

    expect(screen.queryByRole("button", { name: "不使用本地工作区" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "打开本地文件夹" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "按需确认" })).toBeDisabled();
  });

  it("keeps permission selection enabled for an existing task while a response is streaming", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({
      code: 0,
      message: "ok",
      data: {
        status: "active",
        permission_mode: "ask_as_needed",
        permission_version: 3,
        workspace: {
          workspace_id: "workspace-active",
          display_name: "Active Project",
          path: "/Users/alice/Active Project",
          status: "active",
        },
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));

    render(
      <ChatInput
        value=""
        onChange={vi.fn()}
        onSend={mocks.onSend}
        isChatContent
        showHistoryList={false}
        showHistoryButton={false}
        showPromptSuggestions={false}
        showSkillDeposit={false}
        runInBackground
        sessionId="task-streaming"
        isStreaming
      />,
    );

    const permission = await screen.findByRole("button", { name: "按需确认" });
    await waitFor(() => expect(permission).toBeEnabled());
    fireEvent.click(permission);
    expect(screen.getByRole("menu", { name: "权限模式" })).toBeInTheDocument();
  });

  it("keeps permission selection enabled while an existing background task is waiting", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({
      code: 0,
      message: "ok",
      data: {
        status: "active",
        permission_mode: "ask_as_needed",
        permission_version: 3,
        workspace: {
          workspace_id: "workspace-active",
          display_name: "Active Project",
          path: "/Users/alice/Active Project",
          status: "active",
        },
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));

    render(
      <ChatInput
        value=""
        onChange={vi.fn()}
        onSend={mocks.onSend}
        isChatContent
        showHistoryList={false}
        showHistoryButton={false}
        showPromptSuggestions={false}
        showSkillDeposit={false}
        runInBackground
        sessionId="task-waiting"
        disabled
        workspacePermissionDisabled={false}
      />,
    );

    const permission = await screen.findByRole("button", { name: "按需确认" });
    await waitFor(() => expect(permission).toBeEnabled());
  });

  it("hides the unbound workspace trigger when binding metadata has no display name", async () => {
    let resolveWorkspaceLookup!: (response: Response) => void;
    const workspaceLookup = new Promise<Response>((resolve) => {
      resolveWorkspaceLookup = resolve;
    });
    vi.spyOn(globalThis, "fetch").mockReturnValue(workspaceLookup);

    renderComposer(true, "继续任务", "task-with-incomplete-workspace");

    await act(async () => {
      resolveWorkspaceLookup(new Response(JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          status: "active",
          workspace_id: "workspace-incomplete",
          workspace: {
            workspace_id: "workspace-incomplete",
            display_name: "",
            path: "",
            status: "active",
          },
        },
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
      await workspaceLookup;
    });

    expect(screen.getByRole("button", { name: "按需确认" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "不使用本地工作区" })).not.toBeInTheDocument();
  });

  it("enables the default ask-as-needed permission menu only after selecting a workspace", async () => {
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [{
        workspace_id: "workspace-active",
        display_name: "Active Project",
        path: "/Users/alice/Active Project",
        status: "active",
      }] } },
    } as never);
    renderComposer(true);

    const permission = screen.getByRole("button", { name: /按需确认/ });
    expect(permission).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: /Active Project/ }));

    expect(permission).toBeEnabled();
    fireEvent.click(permission);
    expect(screen.getByRole("menu", { name: "权限模式" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "始终询问" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "按需确认" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "全部允许" })).toBeInTheDocument();
  });

  it("clears a selected workspace and disables its permission control", async () => {
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [{
        workspace_id: "workspace-active",
        display_name: "Active Project",
        path: "/Users/alice/Active Project",
        status: "active",
      }] } },
    } as never);
    const success = vi.spyOn(message, "success").mockImplementation(() => ({}) as never);
    renderComposer(true);
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: /Active Project/ }));
    const permission = screen.getByRole("button", { name: /按需确认/ });
    expect(permission).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: /Active Project/ }));
    fireEvent.click(await screen.findByRole("button", { name: "不使用本地工作区" }));
    expect(screen.getByRole("button", { name: "选择工作区" })).toBeInTheDocument();
    expect(permission).toBeDisabled();
    expect(success).toHaveBeenCalledWith("已停用本地工作区");
  });

  it("does not submit a hidden workspace after switching Chat and Work modes", async () => {
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [{
        workspace_id: "workspace-active",
        display_name: "Active Project",
        path: "/Users/alice/Active Project",
        status: "active",
      }] } },
    } as never);
    const { rerender } = renderComposer(true, "执行任务", "temp_task-1");
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: /Active Project/ }));

    rerender(
      <ChatInput value="执行任务" onChange={vi.fn()} onSend={mocks.onSend}
        isChatContent showHistoryList={false} showHistoryButton={false}
        showPromptSuggestions={false} showSkillDeposit={false}
        runInBackground={false} sessionId="temp_task-1" />,
    );
    rerender(
      <ChatInput value="执行任务" onChange={vi.fn()} onSend={mocks.onSend}
        isChatContent showHistoryList={false} showHistoryButton={false}
        showPromptSuggestions={false} showSkillDeposit={false}
        runInBackground sessionId="temp_task-1" />,
    );
    fireEvent.click(screen.getByRole("button", { name: "发送" }));

    expect(mocks.onSend).toHaveBeenCalledOnce();
    expect(mocks.onSend.mock.calls[0][0]).not.toHaveProperty("workspace_id");
    expect(mocks.onSend.mock.calls[0][0]).not.toHaveProperty("workspace_permission_mode");
  });

  it("canceling revoke for another recent workspace preserves current selection", async () => {
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [
        { workspace_id: "one", display_name: "Alpha", path: "/projects/alpha", status: "active" },
        { workspace_id: "two", display_name: "Beta", path: "/projects/beta", status: "active" },
      ] } },
    } as never);
    const { container } = renderComposer(true, "执行任务");
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: /Alpha/ }));
    fireEvent.click(screen.getByRole("button", { name: /Alpha/ }));
    await waitFor(() => {
      expect(container.querySelectorAll(".local-workspace-row-revoke")).toHaveLength(2);
    });
    const revokeButtons = container.querySelectorAll(".local-workspace-row-revoke");
    fireEvent.click(revokeButtons[1]);
    expect(screen.getByRole("dialog", { name: "撤销工作区授权？" })).toHaveTextContent("/projects/beta");
    fireEvent.click(screen.getByRole("button", { name: "取消" }));

    expect(screen.getByRole("button", { name: /Alpha/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    expect(mocks.onSend).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: "one" }));
  });

  it("sends the selected task permission mode and keeps workspace and permission menus mutually exclusive", async () => {
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [{
        workspace_id: "workspace-active",
        display_name: "Active Project",
        path: "/Users/alice/Active Project",
        status: "active",
      }] } },
    } as never);
    renderComposer(true, "执行任务");
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: /Active Project/ }));
    fireEvent.click(screen.getByRole("button", { name: /按需确认/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "始终询问" }));

    fireEvent.click(screen.getByRole("button", { name: /Active Project/ }));
    expect(screen.getByRole("searchbox", { name: "搜索工作区" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /始终询问/ }));
    expect(screen.queryByRole("searchbox", { name: "搜索工作区" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    expect(mocks.onSend).toHaveBeenCalledWith(expect.objectContaining({
      workspace_id: "workspace-active",
      workspace_permission_mode: "always_ask",
    }));
  });

  it("requires an explicit four-risk confirmation before enabling allow-all", async () => {
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [{
        workspace_id: "workspace-active",
        display_name: "Active Project",
        path: "/Users/alice/Active Project",
        status: "active",
      }] } },
    } as never);
    renderComposer(true);
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: /Active Project/ }));
    fireEvent.click(screen.getByRole("button", { name: /按需确认/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "全部允许" }));

    const dialog = screen.getByRole("dialog", { name: "要开启“全部允许”吗？" });
    expect(dialog).toHaveTextContent("文件风险");
    expect(dialog).toHaveTextContent("命令风险");
    expect(dialog).toHaveTextContent("联网风险");
    expect(dialog).toHaveTextContent("已连接应用风险");
    expect(dialog.querySelectorAll("li")).toHaveLength(4);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "要开启“全部允许”吗？" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /按需确认/ })).toBeInTheDocument();
  });

  it("switches to allow-all only after confirmation and reports success", async () => {
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [{
        workspace_id: "workspace-active",
        display_name: "Active Project",
        path: "/Users/alice/Active Project",
        status: "active",
      }] } },
    } as never);
    const success = vi.spyOn(message, "success").mockImplementation(() => ({}) as never);
    renderComposer(true);
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: /Active Project/ }));
    fireEvent.click(screen.getByRole("button", { name: /按需确认/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "全部允许" }));

    expect(screen.getByRole("button", { name: /按需确认/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "确认开启" }));
    expect(screen.getByRole("button", { name: /全部允许/ })).toBeInTheDocument();
    expect(success).toHaveBeenCalledWith("权限已修改");
  });

  it("closes first-use authorization on backdrop or Escape without changing workspace", async () => {
    mocks.selectLocalWorkspace.mockResolvedValue({
      canceled: false,
      selection_token: "selection-token",
      display_name: "Documents",
      path: "/Users/alice/Documents",
    });
    renderComposer(true);
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: "打开本地文件夹" }));
    const dialog = await screen.findByRole("dialog", { name: "首次使用时需要授权" });
    fireEvent.keyDown(document, { key: "Escape" });
    expect(dialog).not.toBeInTheDocument();
    expect(mocks.authorizeLocalWorkspace).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "选择工作区" })).toBeInTheDocument();
  });

  it("opens authorization for an inactive recent workspace and reports successful changes", async () => {
    mocks.reauthorizeLocalWorkspace.mockResolvedValue({
      canceled: false,
      selection_token: "reauthorization-token",
      display_name: "Old Project",
      path: "/Users/alice/Old Project",
    });
    vi.spyOn(axiosInstance, "get").mockResolvedValue({
      data: { data: { items: [{
        workspace_id: "workspace-revoked",
        display_name: "Old Project",
        path: "/Users/alice/Old Project",
        status: "revoked",
      }] } },
    } as never);
    const success = vi.spyOn(message, "success").mockImplementation(() => ({}) as never);
    renderComposer(true);
    fireEvent.click(screen.getByRole("button", { name: "选择工作区" }));
    fireEvent.click(await screen.findByRole("button", { name: /Old Project/ }));

    expect(await screen.findByRole("dialog", { name: "首次使用时需要授权" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "选择工作区" })).toBeInTheDocument();
    expect(success).not.toHaveBeenCalled();
  });
});
