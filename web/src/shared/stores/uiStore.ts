import { create } from 'zustand'
type UiStore = { sidebarOpen:boolean; setSidebarOpen:(value:boolean)=>void }
export const useUiStore = create<UiStore>((set) => ({ sidebarOpen: true, setSidebarOpen: (sidebarOpen) => set({ sidebarOpen }) }))
