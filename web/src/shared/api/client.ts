export type Kpi = { key: string; label: string; value: number }
export type Summary = { kpis: Kpi[]; freshness: { status: string; message: string } }
export type CreatorStatus = 'ACTIVE'|'ON_LEAVE'|'DISMISSED'
export type Creator = { id:string; firstName:string; lastName:string; middleName:string; displayName:string; status:CreatorStatus; createdAt:string }
export type Timeseries = { items: { date:string; views:number }[] }
export type Publication = { id:string; title:string | null; platform:string; publishedAt:string; creatorName:string; views:number }
export type CreatorAnalytics = { creatorId:string; creatorName:string; period:{from:string;to:string}; kpis:Kpi[]; publications:{id:string;title:string;platform:string;publishedAt:string;views:number;likes:number}[] }
export type CreatorDetail = Creator & { internalNote:string; telegramUsername:string; contacts:{id:string;kind:string;value:string;label:string;isPrimary:boolean}[] }
export type CreatorCredential = { id:string; section:string; fieldKey:string; isSecret:boolean; hasValue:boolean; value?:string; updatedAt:string }
export type PlatformAccount = {id:string;platform:string;username:string;displayName:string;status:string;profileUrl:string}
export type Platform = 'YOUTUBE' | 'INSTAGRAM' | 'TIKTOK' | 'VK'
export type PlatformConnection = {id:string;platform:Platform;username:string;displayName:string;status:string;oauthStatus:string;avatarUrl:string;profileUrl:string;scopes:string[];lastSyncedAt:string | null}
export type IntegrationStatus = {id:Platform;name:string;configured:boolean;connectedAccounts:number}
export type SyncAccount = {
  id:string
  platform:Platform
  username:string
  displayName:string
  profileUrl:string
  creatorId:string
  creatorName:string
  accountStatus:string
  oauthStatus:string
  health:'HEALTHY'|'WARNING'|'ERROR'|'PENDING'
  message:string
  lastSyncedAt:string|null
  tokenExpiresAt:string|null
  consecutiveFailures:number
  lastSuccessAt:string|null
}
export type ContentGroup = {id:string;name:string;status:string;creatorName:string;publicationCount:number}
export class ApiError extends Error { constructor(public status:number, message:string) { super(message) } }
export async function request<T>(path:string, options:RequestInit = {}): Promise<T> { const response=await fetch(`/api/v1${path}`, { credentials:'include', headers:{'Content-Type':'application/json', ...options.headers}, ...options }); if (!response.ok) { const error=await response.json().catch(()=>({detail:'Ошибка запроса'})); throw new ApiError(response.status,error.detail ?? error.title) }; if(response.status===204) return undefined as T; return response.json() as Promise<T> }
export const api = {
  login:(email:string,password:string)=>request('/auth/login',{method:'POST',body:JSON.stringify({email,password})}),
  logout:()=>request('/auth/logout',{method:'POST'}),
  me:()=>request<{id:string;email:string;role:string}>('/auth/me'),
  acceptInvitation:(token:string,password:string)=>request<{email:string}>('/auth/accept-invitation',{method:'POST',body:JSON.stringify({token,password})}),
  createInvitation:(email:string,role:string)=>request<{acceptanceUrl:string}>('/users/invitations',{method:'POST',body:JSON.stringify({email,role})}),
  summary:()=>request<Summary>('/analytics/summary'),
  timeseries:()=>request<Timeseries>('/analytics/timeseries'),
  creators:()=>request<{items:Creator[]}>('/creators'),
  contentGroups:()=>request<{items:ContentGroup[]}>('/content-groups'),
  creator:(id:string)=>request<CreatorDetail>(`/creators/${id}`),
  updateCreator:(id:string,payload:Record<string,string>)=>request<void>(`/creators/${id}`,{method:'PATCH',body:JSON.stringify(payload)}),
  creatorCredentials:(id:string)=>request<{items:CreatorCredential[]}>(`/creators/${id}/credentials`),
  saveCreatorCredentials:(id:string,items:{section:string;fieldKey:string;value:string}[])=>request<void>(`/creators/${id}/credentials`,{method:'PUT',body:JSON.stringify({items})}),
  revealCreatorCredential:(id:string,credentialID:string)=>request<{value:string}>(`/creators/${id}/credentials/${credentialID}/reveal`,{method:'POST'}),
  creatorAccounts:(id:string)=>request<{items:PlatformAccount[]}>(`/creators/${id}/accounts`),
  createCreatorAccount:(id:string,payload:Record<string,string>)=>request(`/creators/${id}/accounts`,{method:'POST',body:JSON.stringify(payload)}),
  creatorAnalytics:(id:string,from:string,to:string)=>request<CreatorAnalytics>(`/analytics/creators/${id}?activityFrom=${encodeURIComponent(from)}&activityTo=${encodeURIComponent(to)}`),
  createCreator:(payload:Record<string,string>)=>request('/creators',{method:'POST',body:JSON.stringify(payload)}),
  createContact:(id:string,payload:Record<string,unknown>)=>request(`/creators/${id}/contacts`,{method:'POST',body:JSON.stringify(payload)}),
  publications:()=>request<{items:Publication[]}>('/publications'),
  syncHealth:()=>request<{status:string;dueTargets:number}>('/sync/health'),
  connections:(id:string)=>request<{items:PlatformConnection[]}>(`/creators/${id}/connections`),
  integrations:()=>request<{items:IntegrationStatus[];accounts:SyncAccount[]}>('/integrations'),
  startAuthorization:(id:string,platform:Platform)=>request<{authorizationUrl:string;expiresAt:string}>(`/creators/${id}/connections/${platform.toLowerCase()}/authorize`,{method:'POST'}),
  purgePlatformData:(id:string)=>request<void>(`/platform-accounts/${id}/data`,{method:'DELETE'}),
}
