export type Kpi = { key: string; label: string; value: number }
export type Summary = { kpis: Kpi[]; freshness: { status: string; message: string } }
export type CreatorStatus = 'ACTIVE'|'ON_LEAVE'|'DISMISSED'|'ARCHIVED'
export type CreatorWorkStatus = 'OK'|'NEEDS_ATTENTION'|'IN_PROGRESS'
export type Company = { id:string; name:string; creatorCount:number; hasVkAccount:boolean }
export type ArchivedCompany = { id:string; name:string; archivedAt:string; purgeAt:string; remainingSeconds:number; creatorCount:number }
export type CompanyVkAccount = { id:string; companyId:string; companyName:string; login:string; phone:string; hasPassword:boolean; accessMethod:'LOGIN'|'PHONE'; updatedAt:string; oauthDisplayName:string; oauthUsername:string; oauthAvatarUrl:string; oauthProfileUrl:string; oauthStatus:string; platformAccountId:string; lastSyncedAt:string|null; lastSuccessAt:string|null; syncError:string; consecutiveFailures:number; communityCount:number }
export type CreatorVkAccess = { accountId:string; companyId:string; companyName:string; login:string; phone:string; hasPassword:boolean; accessMethod:'LOGIN'|'PHONE'|''; communityUrl:string; recipientAccountUrl:string }
export type Creator = { id:string; firstName:string; lastName:string; middleName:string; displayName:string; status:CreatorStatus; createdAt:string; archivedAt:string|null; telegramUsername:string; companyId:string; companyName:string; workStatus:CreatorWorkStatus; workComment:string; connectedPlatforms:Platform[] }
export type Timeseries = { items: { date:string; views:number }[] }
export type Publication = { id:string; title:string | null; platform:Platform; publishedAt:string; thumbnailUrl:string | null; permalink:string | null; creatorName:string; companyId:string; companyName:string; views:number; likes:number; comments:number; shares:number }
export type CreatorAnalytics = { creatorId:string; creatorName:string; period:{from:string;to:string}; kpis:Kpi[]; publications:{id:string;title:string;platform:string;publishedAt:string;views:number;likes:number}[] }
export type CreatorDetail = Creator & { internalNote:string; canDelete:boolean; contacts:{id:string;kind:string;value:string;label:string;isPrimary:boolean}[] }
export type CreatorCredential = { id:string; section:string; fieldKey:string; isSecret:boolean; hasValue:boolean; value?:string; updatedAt:string }
export type CreatorHistoryBlock = 'PROFILE'|'WORK'|'CREDENTIALS'
export type CreatorHistoryChange = { id:string; section:string; fieldKey:string; isSecret:boolean; oldPresent:boolean; newPresent:boolean; oldValue?:string; newValue?:string }
export type CreatorHistoryEvent = { id:string; changedAt:string; changedBy:string; changes:CreatorHistoryChange[] }
export type PlatformAccount = {id:string;platform:string;username:string;displayName:string;status:string;profileUrl:string}
export type Platform = 'YOUTUBE' | 'INSTAGRAM' | 'TIKTOK' | 'VK'
const officialPublicationPaths: Record<Platform, { host:string; path:RegExp }> = {
  YOUTUBE:{host:'www.youtube.com',path:/^\/shorts\/[A-Za-z0-9_-]{11}$/},
  INSTAGRAM:{host:'www.instagram.com',path:/^\/reel\/[A-Za-z0-9_-]{5,64}\/$/},
  VK:{host:'vk.ru',path:/^\/video-?[1-9][0-9]{0,18}_[1-9][0-9]{0,18}$/},
  TIKTOK:{host:'www.tiktok.com',path:/^\/@[A-Za-z0-9._]{2,24}\/video\/[0-9]{6,32}$/},
}
export function safePublicationPermalink(platform:Platform,value:string|null|undefined){if(!value)return undefined;try{const parsed=new URL(value);const rule=officialPublicationPaths[platform];if(parsed.protocol!=='https:'||parsed.username||parsed.password||parsed.port||parsed.search||parsed.hash||parsed.hostname.toLowerCase()!==rule.host||!rule.path.test(parsed.pathname))return undefined;return parsed.href}catch{return undefined}}
export type PlatformConnection = {id:string;platform:Platform;username:string;displayName:string;status:string;oauthStatus:string;avatarUrl:string;profileUrl:string;scopes:string[];lastSyncedAt:string|null;lastSuccessAt:string|null;syncError:string;consecutiveFailures:number;bioDescription?:string;isVerified?:boolean}
export type InstagramAccountCandidate = {id:string;username:string;displayName:string;avatarUrl:string;facebookPageName:string;connectionState:'AVAILABLE'|'CONNECTED_HERE'|'CONNECTED_ELSEWHERE';connectedCreator?:string;selectable:boolean}
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
export type WorkspaceRole = 'OWNER'|'MANAGER'|'CREATOR'
export type LegacyWorkspaceRole = 'ADMIN'|'ANALYST'|'VIEWER'
export type UserStatus = 'ACTIVE'|'SUSPENDED'
export type ManagerPermission = 'STATS_VIEW'|'STATS_EXPORT'|'CREATOR_CREATE'|'CREATOR_EDIT'|'CREATOR_ARCHIVE'|'CREATOR_DELETE'|'CREATOR_ACCOUNT_MANAGE'|'SOCIAL_CONNECT'|'SYNC_MANAGE'|'CREDENTIAL_EDIT'|'SECRET_REVEAL'
export type ManagerCompanyAssignment = {companyId:string;permissions:ManagerPermission[]}
export type WorkspaceUser = {id:string;email:string;role:WorkspaceRole;status:UserStatus;companyAssignments:ManagerCompanyAssignment[]}
export type AuditLog = {id:string;actorId:string|null;companyId:string|null;action:string;entityType:string;entityId:string|null;endpoint:string;httpMethod:string;responseStatus:number|null;metadata:Record<string,unknown>;createdAt:string}
export type AuditFilters = {action?:string;companyId?:string;actorId?:string;entityType?:string;dateFrom?:string;dateTo?:string;limit?:number;offset?:number}
export type SelfDeletionImpact = {mode:'ACCOUNT'|'WORKSPACE';workspaceWillBeDeleted:boolean;counts:{users:number;companies:number;creators:number}}
export type DeletionScheduled = {receiptId:string;status:'DELETING'}
export type CreatorLoginAccount = {id:string;email:string;status:UserStatus}
export type AuthCompany = {id:string;name:string;permissions:ManagerPermission[]}
export type AuthCreatorProfile = {id:string;companyId:string;companyName:string;displayName:string}
export type AuthPrincipal = {id:string;email:string;role:WorkspaceRole|LegacyWorkspaceRole;capabilities:string[];companies:AuthCompany[];creatorProfiles:AuthCreatorProfile[];activeContext:{companyId:string|null;creatorId:string|null;allCompanies:boolean}}
export type OwnCreatorProfileSummary = AuthCreatorProfile & {active:boolean}
export type OwnCreatorProfile = {id:string;companyId:string;companyName:string;firstName:string;lastName:string;middleName:string;displayName:string;status:CreatorStatus;telegramUsername:string;workStatus:CreatorWorkStatus;workComment:string;contacts:{id:string;kind:string;value:string;label:string;isPrimary:boolean}[]}
export type OwnSocial = {id:string;platform:Platform;username:string;displayName:string;status:string;avatarUrl?:string;profileUrl:string;communityUrl?:string;recipientAccountUrl?:string;lastSyncedAt?:string|null;bioDescription?:string;isVerified?:boolean}
export type OwnCredential = CreatorCredential
export type OwnPublication = {id:string;title:string;platform:Platform;publishedAt:string;thumbnailUrl:string;permalink:string;views:number;likes:number;comments:number;shares:number}
export type OwnStats = {creatorId:string;creatorName:string;period:{from:string|null;to:string|null};kpis:Kpi[]}
export type ContentTarget = {id:string;platform:Platform;platformAccountId:string;status:string;mediaAssetId:string;externalId?:string;externalUrl?:string;errorCode:string;errorMessage:string;scheduledAt?:string|null;createdAt?:string;updatedAt?:string}
export type ContentItemSummary = {id:string;creatorId:string;creatorName?:string;revision:number;updatedAt:string;scheduledAt?:string;targetCount?:number;attentionCount?:number;platforms?:Platform[]}
export type ContentItem = ContentItemSummary & {companyId:string;description:string;hashtags:string[];platformOptions:Record<string,unknown>;status:string;approval?:{status:string;requester:string;decider:string;note:string;requestedAt:string;decidedAt:string|null};targets:ContentTarget[];availableActions:string[]}
export type ContentAttempt = {id:string;targetId:string;status:string;errorCode:string;errorMessage:string;startedAt:string;finishedAt:string|null}
export type ContentApprovalPolicy = {publishRequiresApproval:boolean;editRequiresApproval:boolean;deleteRequiresApproval:boolean}
export type ContentCommand = 'submit'|'approve'|'reject'|'publish'|'schedule'|'retry'|'cancel'
export type ContentNotification = {id:string;kind:string;contentItemId:string;payload:Record<string,unknown>;readAt:string|null;createdAt:string}
export type ContentDraftInput = {creatorId?:string;description:string;hashtags:string[];platformOptions?:Record<string,unknown>;targets:{platformAccountId:string;platform:Platform;mediaAssetId?:string;options?:Record<string,unknown>}[]}
export type MediaUploadSession = {uploadId:string;mediaAssetId:string;partSize:number;expiresInSeconds:number}
export type ComposerPreflight = {targets:{accountId:string;platform:Platform;preflight:{compatible:boolean;warnings:string[];requiredFields:string[];allowedPrivacy:string[];requiresReauth:boolean;reconnectAction?:string;tiktok?:{privacy:string[];commentsDisabled:boolean;duetDisabled:boolean;stitchDisabled:boolean;warnings:string[];requiredFields:string[];reconnectAction?:string;compatible:boolean}}}[]}
export class ApiError extends Error { constructor(public status:number, message:string) { super(message) } }
export const authorizationDeniedEvent = 'statzavod:authorization-denied'
export async function request<T>(path:string, options:RequestInit = {}): Promise<T> { const locale=getRequestLocale(); const response=await fetch(`/api/v1${path}`, { credentials:'include', headers:{'Content-Type':'application/json','Accept-Language':locale, ...options.headers}, ...options }); if (!response.ok) { if(response.status===403)window.dispatchEvent(new Event(authorizationDeniedEvent)); const error=await response.json().catch(()=>({detail:locale === 'en' ? 'Request failed' : 'Ошибка запроса'})); throw new ApiError(response.status,error.detail ?? error.title) }; if(response.status===204) return undefined as T; return response.json() as Promise<T> }
export async function download(path:string): Promise<Blob> { const locale=getRequestLocale(); const response=await fetch(`/api/v1${path}`,{credentials:'include',headers:{'Accept-Language':locale}}); if(!response.ok){if(response.status===403)window.dispatchEvent(new Event(authorizationDeniedEvent));const error=await response.json().catch(()=>({detail:locale==='en'?'Request failed':'Ошибка запроса'}));throw new ApiError(response.status,error.detail??error.title)};return response.blob() }
export const api = {
  login:(email:string,password:string)=>request('/auth/login',{method:'POST',body:JSON.stringify({email,password})}),
  logout:()=>request('/auth/logout',{method:'POST'}),
  me:()=>request<AuthPrincipal>('/auth/me'),
  setContext:(context:{companyId?:string|null;creatorId?:string|null})=>request<{activeContext:AuthPrincipal['activeContext']}>('/auth/context',{method:'POST',body:JSON.stringify(context)}),
  acceptInvitation:(token:string,password:string)=>request<{email:string}>('/auth/accept-invitation',{method:'POST',body:JSON.stringify({token,password})}),
  createInvitation:(email:string,role:string)=>request<{acceptanceUrl:string}>('/users/invitations',{method:'POST',body:JSON.stringify({email,role})}),
  workspaceUsers:()=>request<{items:WorkspaceUser[]}>('/users'),
  createWorkspaceUser:(payload:{email:string;password:string;role:'OWNER'|'MANAGER';companyAssignments?:ManagerCompanyAssignment[]})=>request<WorkspaceUser>('/users',{method:'POST',body:JSON.stringify(payload)}),
  updateWorkspaceUser:(id:string,payload:{email?:string;status?:UserStatus})=>request<void>(`/users/${id}`,{method:'PATCH',body:JSON.stringify(payload)}),
  resetWorkspaceUserPassword:(id:string,password:string)=>request<void>(`/users/${id}/password`,{method:'PUT',body:JSON.stringify({password})}),
  deleteWorkspaceUser:(id:string)=>request<void>(`/users/${id}`,{method:'DELETE'}),
  replaceManagerCompanyAssignments:(id:string,items:ManagerCompanyAssignment[])=>request<{items:ManagerCompanyAssignment[]}>(`/users/${id}/company-assignments`,{method:'PUT',body:JSON.stringify({items})}),
  selfDeletionImpact:()=>request<SelfDeletionImpact>('/users/me/deletion-impact'),
  selfDelete:(currentPassword:string,confirmEmail:string)=>request<DeletionScheduled|undefined>('/users/me',{method:'DELETE',body:JSON.stringify({currentPassword,confirmEmail})}),
  auditLogs:(filters:AuditFilters={})=>{const params=new URLSearchParams();Object.entries(filters).forEach(([key,value])=>{if(value!==undefined&&value!=='')params.set(key,String(value))});return request<{items:AuditLog[];limit:number;offset:number}>(`/audit${params.size?`?${params}`:''}`)},
  summary:()=>request<Summary>('/analytics/summary'),
  timeseries:()=>request<Timeseries>('/analytics/timeseries'),
  companies:()=>request<{items:Company[]}>('/companies'),
  archivedCompanies:()=>request<{items:ArchivedCompany[]}>('/companies/archive'),
  createCompany:(name:string)=>request<{id:string;name:string}>('/companies',{method:'POST',body:JSON.stringify({name})}),
  archiveCompany:(id:string)=>request<void>(`/companies/${id}`,{method:'DELETE'}),
  restoreCompany:(id:string)=>request<void>(`/companies/${id}/restore`,{method:'POST'}),
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
  deleteCreator:(id:string)=>request<void>(`/creators/${id}`,{method:'DELETE'}),
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
  creatorLoginAccount:(id:string)=>request<{account:CreatorLoginAccount|null}>(`/creators/${id}/login-account`),
  putCreatorLoginAccount:(id:string,email:string,password?:string)=>request<{id:string;email:string;created:boolean}>(`/creators/${id}/login-account`,{method:'PUT',body:JSON.stringify({email,password})}),
  createCreatorAccount:(id:string,payload:Record<string,string>)=>request(`/creators/${id}/accounts`,{method:'POST',body:JSON.stringify(payload)}),
  creatorAnalytics:(id:string,from:string,to:string)=>request<CreatorAnalytics>(`/analytics/creators/${id}?activityFrom=${encodeURIComponent(from)}&activityTo=${encodeURIComponent(to)}`),
  createCreator:(payload:Record<string,string>)=>request('/creators',{method:'POST',body:JSON.stringify(payload)}),
  createContact:(id:string,payload:Record<string,unknown>)=>request(`/creators/${id}/contacts`,{method:'POST',body:JSON.stringify(payload)}),
  publications:()=>request<{items:Publication[]}>('/publications'),
  syncHealth:()=>request<{status:string;dueTargets:number}>('/sync/health'),
  connections:(id:string)=>request<{items:PlatformConnection[]}>(`/creators/${id}/connections`),
  integrations:()=>request<{items:IntegrationStatus[];accounts:SyncAccount[]}>('/integrations'),
  startAuthorization:(id:string,platform:string)=>request<{authorizationUrl:string;expiresAt:string}>(`/creators/${id}/connections/${platform.toLowerCase()}/authorize`,{method:'POST'}),
  instagramAccountSelection:(id:string,selectionID:string)=>request<{items:InstagramAccountCandidate[];expiresAt:string}>(`/creators/${id}/connections/instagram-facebook/selections/${selectionID}`),
  completeInstagramAccountSelection:(id:string,selectionID:string,accountIds:string[])=>request<{connected:number}>(`/creators/${id}/connections/instagram-facebook/selections/${selectionID}`,{method:'POST',body:JSON.stringify({accountIds})}),
  disconnectPlatformAccount:(id:string)=>request<void>(`/platform-accounts/${id}/connection`,{method:'DELETE'}),
  purgePlatformData:(id:string)=>request<void>(`/platform-accounts/${id}/data`,{method:'DELETE'}),
  ownProfiles:()=>request<{items:OwnCreatorProfileSummary[]}>('/creator-portal/profiles'),
  ownProfile:()=>request<OwnCreatorProfile>('/creator-portal/profile'),
  ownSocials:()=>request<{items:OwnSocial[]}>('/creator-portal/socials'),
  ownCredentials:()=>request<{items:OwnCredential[]}>('/creator-portal/credentials'),
  revealOwnCredential:(credentialID:string)=>request<{value:string}>(`/creator-portal/credentials/${encodeURIComponent(credentialID)}/reveal`,{method:'POST'}),
  ownPublications:(from:string,to:string)=>request<{creatorId:string;period:{from:string|null;to:string|null};items:OwnPublication[]}>(`/creator-portal/publications?activityFrom=${encodeURIComponent(from)}&activityTo=${encodeURIComponent(to)}`),
  ownStats:(from:string,to:string)=>request<OwnStats>(`/creator-portal/stats?activityFrom=${encodeURIComponent(from)}&activityTo=${encodeURIComponent(to)}`),
  exportOwnCreator:(from:string,to:string)=>download(`/creator-portal/export?activityFrom=${encodeURIComponent(from)}&activityTo=${encodeURIComponent(to)}`),
  contentItems:(range?:{from:string;until:string})=>request<{items:ContentItemSummary[]}>(`/content-items${range?`?from=${encodeURIComponent(range.from)}&until=${encodeURIComponent(range.until)}`:''}`),
  ownContentItems:(range?:{from:string;until:string})=>request<{items:ContentItemSummary[]}>(`/creator-portal/content-items${range?`?from=${encodeURIComponent(range.from)}&until=${encodeURIComponent(range.until)}`:''}`),
  contentItem:(id:string)=>request<ContentItem>(`/content-items/${id}`),
  ownContentItem:(id:string)=>request<ContentItem>(`/creator-portal/content-items/${id}`),
  contentAttempts:(id:string,own=false,before='')=>request<{items:ContentAttempt[];nextBefore:string}>(`${own?'/creator-portal':''}/content-items/${id}/attempts${before?`?before=${encodeURIComponent(before)}`:''}`),
  createContentItem:(payload:ContentDraftInput,key:string)=>request<{id:string;revision:number;status:string}>('/content-items',{method:'POST',headers:{'Idempotency-Key':key},body:JSON.stringify(payload)}),
  createOwnContentItem:(payload:ContentDraftInput,key:string)=>request<{id:string;revision:number;status:string}>('/creator-portal/content-items',{method:'POST',headers:{'Idempotency-Key':key},body:JSON.stringify(payload)}),
  patchContentItem:(id:string,payload:ContentDraftInput,etag:string)=>request<{id:string;revision:number;status:string}>(`/content-items/${id}`,{method:'PATCH',headers:{'If-Match':etag},body:JSON.stringify(payload)}),
  patchOwnContentItem:(id:string,payload:ContentDraftInput,etag:string)=>request<{id:string;revision:number;status:string}>(`/creator-portal/content-items/${id}`,{method:'PATCH',headers:{'If-Match':etag},body:JSON.stringify(payload)}),
  copyContentItem:(id:string,key:string,own=false)=>request<{id:string;revision:number;status:string}>(`${own?'/creator-portal':''}/content-items/${id}/copy`,{method:'POST',headers:{'Idempotency-Key':key}}),
  commandContentItem:(id:string,action:ContentCommand,scheduledAt?:string,key=crypto.randomUUID(),targetIds?:string[])=>request<{id:string;revision:number;status:string}>(`/content-items/${id}/${action}`,{method:'POST',headers:{'Idempotency-Key':key},body:scheduledAt||targetIds?JSON.stringify({scheduledAt,targetIds}):undefined}),
  commandOwnContentItem:(id:string,action:Exclude<ContentCommand,'approve'|'reject'>,scheduledAt?:string,key=crypto.randomUUID(),targetIds?:string[])=>request<{id:string;revision:number;status:string}>(`/creator-portal/content-items/${id}/${action}`,{method:'POST',headers:{'Idempotency-Key':key},body:scheduledAt||targetIds?JSON.stringify({scheduledAt,targetIds}):undefined}),
  createMediaUpload:(payload:{creatorId?:string;filename:string;mime:string;bytes:number},own=false)=>request<MediaUploadSession>(`${own?'/creator-portal':''}/media/uploads`,{method:'POST',body:JSON.stringify(payload)}),
  signMediaPart:(uploadId:string,partNumber:number,own=false)=>request<{url:string;headers:Record<string,string[]>}>(`${own?'/creator-portal':''}/media/uploads/${uploadId}/parts/${partNumber}`,{method:'POST'}),
  completeMediaUpload:(uploadId:string,parts:{partNumber:number;etag:string}[],own=false)=>request<{mediaAssetId:string;status:string}>(`${own?'/creator-portal':''}/media/uploads/${uploadId}/complete`,{method:'POST',body:JSON.stringify({parts})}),
  abortMediaUpload:(uploadId:string,own=false)=>request<void>(`${own?'/creator-portal':''}/media/uploads/${uploadId}`,{method:'DELETE'}),
  composerPreflight:(payload:{creatorId?:string;accountIds:string[]},own=false)=>request<ComposerPreflight>(`${own?'/creator-portal':''}/content-preflight`,{method:'POST',body:JSON.stringify(payload)}),
  preflightContentItem:(id:string,own=false)=>request<{contentItemId:string;revision:number;targets:{targetId:string;platform:Platform;platformLabel:string;preflight:{compatible:boolean;warnings:string[];requiredFields:string[];allowedPrivacy:string[];requiresReauth:boolean}}[]}>(`${own?'/creator-portal':''}/content-items/${id}/preflight`,{method:'POST'}),
  contentApprovalPolicy:(creatorID:string)=>request<ContentApprovalPolicy>(`/creators/${encodeURIComponent(creatorID)}/content-approval-policy`),
  putContentApprovalPolicy:(creatorID:string,payload:ContentApprovalPolicy)=>request<ContentApprovalPolicy>(`/creators/${encodeURIComponent(creatorID)}/content-approval-policy`,{method:'PUT',body:JSON.stringify(payload)}),
  notifications:(before?:string)=>request<{items:ContentNotification[];unreadCount:number;nextBefore:string}>(`/notifications${before?`?before=${encodeURIComponent(before)}`:''}`),
  markNotificationRead:(id:string)=>request<{status:string}>(`/notifications/${id}/read`,{method:'POST'}),
  markAllNotificationsRead:()=>request<{status:string}>('/notifications/read-all',{method:'POST'}),
}
import { getRequestLocale } from '../i18n/locale'
