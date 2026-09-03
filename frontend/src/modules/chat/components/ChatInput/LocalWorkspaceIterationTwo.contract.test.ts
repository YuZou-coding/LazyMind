import { describe, expect, it } from "vitest";

import enUS from "@/i18n/locales/en-US";
import zhCN from "@/i18n/locales/zh-CN";

describe("iteration two local workspace copy contract", () => {
  it("states the authorized file operations and controlled cleanup scope exactly", () => {
    expect(zhCN.chat.workspace.authorizationScope).toBe(
      "授权仅适用于该文件夹及其子目录。任务可直接读取、创建和修改文件；受控工具可能更新或清理工作区内的构建、依赖和缓存文件。",
    );
  });

  it("does not tell users that ordinary writes require another confirmation", () => {
    expect(zhCN.chat.workspace.authorizationScope).not.toContain("修改文件前询问");
    expect(enUS.chat.workspace.authorizationScope).not.toContain("asks before modifying files");
  });
});
