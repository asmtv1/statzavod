export type Kpi = { key: string; label: string; value: number }
export type Summary = { kpis: Kpi[]; freshness: { status: string; message: string } }
export type CreatorStatus = 'ACTIVE'|'ON_LEAVE'|'DISMISSED'|'ARCHIVED'
export type CreatorWorkStatus = 'OK'|'NEEDS_ATTENTION'|'IN_PROGRESS'
export type Company = { id:string; name:string; creatorCount:number; hasVkAccount:boolean }
export type CompanyVkAccount = { id:string; companyId:string; companyName:string; login:string; phone:string; hasPassword:boolean; accessMethod:'LOGIN'|'PHONE'; updatedAt:string; oauthDisplayName:string; oauthUsername:string; oauthAvatarUrl:string; oauthProfileUrl:string; oauthStatus:string; platformAccountId:string; lastSyncedAt:string|null; lastSuccessAt:string|null; syncError:string; consecutiveFailures:number; communityCount:number }
export type CreatorVkAccess = { accountId:string; companyId:string; companyName:string; login:string; phone:string; hasPassword:boolean; accessMethod:'LOGIN'|'PHONE'|''; communityUrl:string; recipientAccountUrl:string }
export type Creator = { id:string; firstName:string; lastName:string; middleName:string; displayName:string; status:CreatorStatus; createdAt:string; archivedAt:string|null; telegramUsername:string; companyId:string; companyName:string; workStatus:CreatorWorkStatus; workComment:string; connectedPlatforms:Platform[] }
export type Timeseries = { items: { date:string; views:number }[] }
export type Publication = { id:string; title:string | null; platform:string; publishedAt:string; creatorName:string; companyId:string; companyName:string; views:number }
export type CreatorAnalytics = { creatorId:string; creatorName:string; period:{from:string;to:string}; kpis:Kpi[]; publications:{id:string;title:string;platform:string;publishedAt:string;views:number;likes:number}[] }
export type CreatorDetail = Creator & { internalNote:string; contacts:{id:string;kind:string;value:string;label:string;isPrimary:boolean}[] }
export type CreatorCredential = { id:string; section:string; fieldKey:string; isSecret:boolean; hasValue:boolean; value?:string; updatedAt:string }
export type CreatorHistoryBlock = 'PROFILE'|'WORK'|'CREDENTIALS'
export type CreatorHistoryChange = { id:string; section:string; fieldKey:string; isSecret:boolean; oldPresent:boolean; newPresent:boolean; oldValue?:string; newValue?:string }
export type CreatorHistoryEvent = { id:string; changedAt:string; changedBy:string; changes:CreatorHistoryChange[] }
export type PlatformAccount = {id:string;platform:string;username:string;displayName:string;status:string;profileUrl:string}
export type Platform = 'YOUTUBE' | 'INSTAGRAM' | 'TIKTOK' | 'VK'
export type PlatformConnection = {id:string;platform:Platform;username:string;displayName:string;status:string;oauthStatus:string;avatarUrl:string;profileUrl:string;scopes:string[];lastSyncedAt:string | null}
export type InstagramConnectionInvitation = {id:string;expiresAt:string;createdAt?:string}
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
  companies:()=>request<{items:Company[]}>('/companies'),
  createCompany:(name:string)=>request<{id:string;name:string}>('/companies',{method:'POST',body:JSON.stringify({name})}),
  archiveCompany:(id:string)=>request<void>(`/companies/${id}`,{method:'DELETE'}),
  companyVkAccounts:()=>request<{items:CompanyVkAccount[]}>('/company-vk-accounts'),
  saveCompanyVkAccount:(companyId:string,payload:{accessMethod:'LOGIN'|'PHONE';login:string;password:string;phone:string})=>request<{id:string}>(`/companies/${companyId}/vk-account`,{method:'PUT',body:JSON.stringify(payload)}),
  startCompanyVkAuthorization:(companyId:string)=>request<{authorizationUrl:string;expiresAt:string}>(`/companies/${companyId}/vk-account/authorize`,{method:'POST'}),
  requestPlatformSync:(platformAccountId:string)=>request<void>(`/platform-accounts/${platformAccountId}/sync`,{method:'POST'}),
  revealCompanyVkPassword:(accountId:string)=>request<{value:string}>(`/company-vk-accounts/${accountId}/password/reveal`,{method:'POST'}),
  creators:()=>request<{items:Creator[]}>('/creators'),
  archivedCreators:()=>request<{items:Creator[]}>('/creators?scope=archived'),
  contentGroups:()=>request<{items:ContentGroup[]}>('/content-groups'),
  creator:(id:string)=>request<CreatorDetail>(`/creators/${id}`),
  updateCreator:(id:string,payload:Record<string,string>)=>request<void>(`/creators/${id}`,{method:'PATCH',body:JSON.stringify(payload)}),
  archiveCreator:(id:string)=>request<void>(`/creators/${id}/archive`,{method:'POST'}),
  restoreCreator:(id:string)=>request<void>(`/creators/${id}/restore`,{method:'POST'}),
  updateCreatorWorkStatus:(id:string,status:CreatorWorkStatus,comment:string)=>request<void>(`/creators/${id}/work-status`,{method:'PATCH',body:JSON.stringify({status,comment})}),
  creatorHistory:(id:string,block:CreatorHistoryBlock)=>request<{items:CreatorHistoryEvent[]}>(`/creators/${id}/history?block=${encodeURIComponent(block)}`),
  revealCreatorHistoryCredential:(id:string,changeID:string,side:'old'|'new')=>request<{value:string}>(`/creators/${id}/history/changes/${changeID}/reveal`,{method:'POST',body:JSON.stringify({side})}),
  creatorCredentials:(id:string)=>request<{items:CreatorCredential[]}>(`/creators/${id}/credentials`),
  saveCreatorCredentials:(id:string,items:{section:string;fieldKey:string;value:string}[])=>request<void>(`/creators/${id}/credentials`,{method:'PUT',body:JSON.stringify({items})}),
  revealCreatorCredential:(id:string,credentialID:string)=>request<{value:string}>(`/creators/${id}/credentials/${credentialID}/reveal`,{method:'POST'}),
  creatorVkAccess:(id:string)=>request<CreatorVkAccess>(`/creators/${id}/vk-access`),
  saveCreatorVkAccess:(id:string,accountId:string,communityUrl:string,recipientAccountUrl:string)=>request<void>(`/creators/${id}/vk-access`,{method:'PUT',body:JSON.stringify({accountId,communityUrl,recipientAccountUrl})}),
  creatorAccounts:(id:string)=>request<{items:PlatformAccount[]}>(`/creators/${id}/accounts`),
  createCreatorAccount:(id:string,payload:Record<string,string>)=>request(`/creators/${id}/accounts`,{method:'POST',body:JSON.stringify(payload)}),
  creatorAnalytics:(id:string,from:string,to:string)=>request<CreatorAnalytics>(`/analytics/creators/${id}?activityFrom=${encodeURIComponent(from)}&activityTo=${encodeURIComponent(to)}`),
  createCreator:(payload:Record<string,string>)=>request('/creators',{method:'POST',body:JSON.stringify(payload)}),
  createContact:(id:string,payload:Record<string,unknown>)=>request(`/creators/${id}/contacts`,{method:'POST',body:JSON.stringify(payload)}),
  publications:()=>request<{items:Publication[]}>('/publications'),
  syncHealth:()=>request<{status:string;dueTargets:number}>('/sync/health'),
  connections:(id:string)=>request<{items:PlatformConnection[]}>(`/creators/${id}/connections`),
  integrations:()=>request<{items:IntegrationStatus[];accounts:SyncAccount[]}>('/integrations'),
  startAuthorization:(id:string,platform:string)=>request<{authorizationUrl:string;expiresAt:string}>(`/creators/${id}/connections/${platform.toLowerCase()}/authorize`,{method:'POST'}),
  instagramConnectionInvitation:(id:string)=>request<{invitation:InstagramConnectionInvitation|null}>(`/creators/${id}/connections/instagram/invitation`),
  createInstagramConnectionInvitation:(id:string)=>request<{id:string;connectionUrl:string;expiresAt:string}>(`/creators/${id}/connections/instagram/invitations`,{method:'POST'}),
  revokeInstagramConnectionInvitation:(creatorId:string,invitationId:string)=>request<void>(`/creators/${creatorId}/connections/instagram/invitations/${invitationId}`,{method:'DELETE'}),
  instagramConnectionInvitationInfo:(token:string)=>request<{creatorName:string;expiresAt:string}>(`/oauth/connection-invitations/instagram/${encodeURIComponent(token)}`),
  instagramConnectionInvitationAuthorizationUrl:(token:string)=>`/api/v1/oauth/connection-invitations/instagram/${encodeURIComponent(token)}/authorize`,
  purgePlatformData:(id:string)=>request<void>(`/platform-accounts/${id}/data`,{method:'DELETE'}),
}
