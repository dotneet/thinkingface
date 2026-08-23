import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ApiResult } from "@/lib/api";
import {
  addMember,
  canAdminOrg,
  isOrgMember,
  isOrgRole,
  listAuditLog,
  listOrgs,
  ORG_ROLES,
  orgErrorKey,
  orgHref,
  removeMember,
  updateMemberRole,
} from "@/lib/orgs";
import type { Org } from "@/types/api";

type Call = { url: string; method: string; body?: string };

function mockFetch(status = 200, body: unknown = {}): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({
        url,
        method: init?.method ?? "GET",
        body: typeof init?.body === "string" ? init.body : undefined,
      });
      return new Response(status === 204 ? null : JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

function failure(status: number, type?: string): Extract<ApiResult<unknown>, { ok: false }> {
  return { ok: false, status, message: "server message", type };
}

function org(overrides: Partial<Org> = {}): Org {
  return {
    name: "acme",
    display_name: "",
    description: "",
    website: "",
    avatar_url: "",
    members_visibility: "members",
    num_members: 1,
    num_repos: 0,
    created_at: "2026-01-01T00:00:00Z",
    viewer_role: "read",
    ...overrides,
  };
}

beforeEach(() => {
  vi.stubEnv("API_URL", "http://localhost:8080");
  vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("endpoints", () => {
  it("builds the directory query, dropping empty parameters", async () => {
    const calls = mockFetch(200, { items: [], total: 0 });
    await listOrgs({ search: "acme", limit: 30 });
    expect(calls[0]?.url).toBe("http://localhost:8080/api/v1/orgs?search=acme&limit=30");
    await listOrgs({ search: "", limit: 30, offset: 60 });
    expect(calls[1]?.url).toBe("http://localhost:8080/api/v1/orgs?limit=30&offset=60");
  });

  it("percent-encodes names and usernames in the path", async () => {
    const calls = mockFetch(204);
    await removeMember("a b", "c/d");
    expect(calls[0]?.url).toBe("http://localhost:8080/api/v1/orgs/a%20b/members/c%2Fd");
    expect(calls[0]?.method).toBe("DELETE");
  });

  it("PATCHes a role change with the role in the body", async () => {
    const calls = mockFetch(200, { member: {} });
    await updateMemberRole("acme", "alice", "write");
    expect(calls[0]?.method).toBe("PATCH");
    expect(calls[0]?.body).toBe(JSON.stringify({ role: "write" }));
  });

  it("POSTs a member add", async () => {
    const calls = mockFetch(201, { member: {} });
    await addMember("acme", { username: "alice", role: "admin" });
    expect(calls[0]?.url).toBe("http://localhost:8080/api/v1/orgs/acme/members");
    expect(calls[0]?.body).toBe(JSON.stringify({ username: "alice", role: "admin" }));
  });

  it("omits a zero audit-log cursor, which would mean nothing to the server", async () => {
    const calls = mockFetch(200, { items: [], next_before: 0 });
    await listAuditLog("acme", { before: 0, limit: 50 });
    expect(calls[0]?.url).toBe("http://localhost:8080/api/v1/orgs/acme/audit-log?limit=50");
    await listAuditLog("acme", { before: 42, limit: 50 });
    expect(calls[1]?.url).toBe(
      "http://localhost:8080/api/v1/orgs/acme/audit-log?before=42&limit=50",
    );
  });

  it("never throws when the backend is unreachable (CLAUDE.md invariant 3)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("ECONNREFUSED");
      }),
    );
    const result = await listOrgs();
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.status).toBe(0);
  });

  it("surfaces the error type from the body", async () => {
    mockFetch(409, { error: { type: "last_admin", message: "nope" } });
    const result = await removeMember("acme", "alice");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.type).toBe("last_admin");
  });
});

describe("orgErrorKey", () => {
  it("maps every documented error type (§7.1)", () => {
    expect(orgErrorKey(failure(403, "org_creation_disabled"))).toBe("org.errors.creationDisabled");
    expect(orgErrorKey(failure(400, "reserved_name"))).toBe("org.errors.nameReserved");
    expect(orgErrorKey(failure(409, "last_admin"))).toBe("org.errors.lastAdmin");
    expect(orgErrorKey(failure(409, "has_repositories"))).toBe("org.errors.hasRepositories");
    expect(orgErrorKey(failure(409, "already_member"))).toBe("org.errors.alreadyMember");
  });

  it("falls back to status for a typeless failure", () => {
    expect(orgErrorKey(failure(401))).toBe("org.errors.loginRequired");
    expect(orgErrorKey(failure(403))).toBe("org.errors.permissionDenied");
  });

  it("returns null when only the server's own message can explain it", () => {
    expect(orgErrorKey(failure(404))).toBeNull();
    expect(orgErrorKey(failure(500))).toBeNull();
    expect(orgErrorKey(failure(400, "something_new"))).toBeNull();
  });

  it("lets a call site name what its ambiguous statuses mean", () => {
    expect(orgErrorKey(failure(404), { 404: "org.errors.userNotFound" })).toBe(
      "org.errors.userNotFound",
    );
  });

  it("prefers the type over the status fallback", () => {
    expect(
      orgErrorKey(failure(403, "org_creation_disabled"), { 403: "org.errors.lastAdmin" }),
    ).toBe("org.errors.creationDisabled");
  });
});

describe("role helpers", () => {
  it("treats the empty viewer_role as 'not a member'", () => {
    expect(isOrgMember(org({ viewer_role: "" as Org["viewer_role"] }))).toBe(false);
    expect(isOrgMember(org({ viewer_role: "read" }))).toBe(true);
  });

  it("only admin may open settings", () => {
    expect(canAdminOrg(org({ viewer_role: "admin" }))).toBe(true);
    expect(canAdminOrg(org({ viewer_role: "write" }))).toBe(false);
    expect(canAdminOrg(org({ viewer_role: "" as Org["viewer_role"] }))).toBe(false);
  });

  it("narrows a select value to a role", () => {
    expect(isOrgRole("admin")).toBe(true);
    expect(isOrgRole("owner")).toBe(false);
    expect(isOrgRole("")).toBe(false);
  });

  it("lists the roles strongest first", () => {
    expect([...ORG_ROLES]).toEqual(["admin", "write", "read"]);
  });

  it("encodes the profile path", () => {
    expect(orgHref("acme")).toBe("/orgs/acme");
    expect(orgHref("a b")).toBe("/orgs/a%20b");
  });
});
