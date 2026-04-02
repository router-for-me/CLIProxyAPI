/**
 * Account Pool management API
 */

import { apiClient } from './client';

export interface MemberAccount {
  id: number;
  email: string;
  password: string;
  recovery_email: string;
  totp_secret: string;
  status: string;
  nstbrowser_profile_id: string;
  nstbrowser_profile_name: string;
  created_at?: string;
  updated_at?: string;
}

export interface LeaderAccount extends MemberAccount {
  ultra_subscription_expiry?: string | null;
}

export interface PoolProxy {
  id: number;
  proxy_url: string;
  type: string;
  status: string;
  created_at?: string;
  updated_at?: string;
}

export interface AccountGroup {
  id: number;
  group_id: string;
  date: string;
  leader_email: string;
  member_email: string;
  family_status: string;
  created_at?: string;
  updated_at?: string;
}

export interface ListResponse<T> {
  items: T[] | null;
  total: number;
  limit: number;
  offset: number;
}

export interface BatchImportResponse {
  created: number;
  errors: string[] | null;
  total_lines: number;
}

const BASE = '/account-pool';

export const accountPoolApi = {
  // Members
  listMembers: (params?: { status?: string; search?: string; limit?: number; offset?: number }) =>
    apiClient.get<ListResponse<MemberAccount>>(`${BASE}/members`, { params }),

  getMember: (id: number) =>
    apiClient.get<MemberAccount>(`${BASE}/members/${id}`),

  createMember: (data: MemberAccount) =>
    apiClient.post<MemberAccount>(`${BASE}/members`, data),

  updateMember: (id: number, data: Partial<MemberAccount>) =>
    apiClient.put<MemberAccount>(`${BASE}/members/${id}`, data),

  updateMemberStatus: (id: number, status: string) =>
    apiClient.patch<MemberAccount>(`${BASE}/members/${id}/status`, { status }),

  deleteMember: (id: number) =>
    apiClient.delete<void>(`${BASE}/members/${id}`),

  batchImportMembers: (text: string) =>
    apiClient.post<BatchImportResponse>(`${BASE}/members/batch`, { text }),

  pickNextMember: () =>
    apiClient.post<MemberAccount>(`${BASE}/members/pick`),

  // Leaders
  listLeaders: (params?: { status?: string; search?: string; limit?: number; offset?: number }) =>
    apiClient.get<ListResponse<LeaderAccount>>(`${BASE}/leaders`, { params }),

  getLeader: (id: number) =>
    apiClient.get<LeaderAccount>(`${BASE}/leaders/${id}`),

  createLeader: (data: LeaderAccount) =>
    apiClient.post<LeaderAccount>(`${BASE}/leaders`, data),

  updateLeader: (id: number, data: Partial<LeaderAccount>) =>
    apiClient.put<LeaderAccount>(`${BASE}/leaders/${id}`, data),

  updateLeaderStatus: (id: number, status: string) =>
    apiClient.patch<LeaderAccount>(`${BASE}/leaders/${id}/status`, { status }),

  deleteLeader: (id: number) =>
    apiClient.delete<void>(`${BASE}/leaders/${id}`),

  batchImportLeaders: (text: string) =>
    apiClient.post<BatchImportResponse>(`${BASE}/leaders/batch`, { text }),

  // Proxies
  listProxies: (params?: { status?: string; type?: string; limit?: number; offset?: number }) =>
    apiClient.get<ListResponse<PoolProxy>>(`${BASE}/proxies`, { params }),

  createProxy: (data: Partial<PoolProxy>) =>
    apiClient.post<PoolProxy>(`${BASE}/proxies`, data),

  updateProxy: (id: number, data: Partial<PoolProxy>) =>
    apiClient.put<PoolProxy>(`${BASE}/proxies/${id}`, data),

  deleteProxy: (id: number) =>
    apiClient.delete<void>(`${BASE}/proxies/${id}`),

  batchImportProxies: (text: string, type?: string) =>
    apiClient.post<BatchImportResponse>(`${BASE}/proxies/batch`, { text, type }),

  pickNextProxy: (type?: string) =>
    apiClient.post<PoolProxy>(`${BASE}/proxies/pick`, { type }),

  // Groups
  listGroups: (params?: { limit?: number; offset?: number; search?: string }) =>
    apiClient.get<ListResponse<AccountGroup>>(`${BASE}/groups`, { params }),

  createGroup: (data: Partial<AccountGroup>) =>
    apiClient.post<AccountGroup>(`${BASE}/groups`, data),

  updateGroup: (id: number, data: Partial<AccountGroup>) =>
    apiClient.put<AccountGroup>(`${BASE}/groups/${id}`, data),

  deleteGroup: (id: number) =>
    apiClient.delete<void>(`${BASE}/groups/${id}`)
};
