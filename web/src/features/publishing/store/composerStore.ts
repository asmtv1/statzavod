import { create } from 'zustand'

// Files and object URLs intentionally never enter this store: they can be large,
// non-serializable and must disappear with the browser session.
type ComposerState = {
  progress: number
  uploadID: string | null
  setUpload: (uploadID: string | null, progress?: number) => void
  setProgress: (progress: number) => void
  resetUpload: () => void
}

export const useComposerStore = create<ComposerState>(set => ({
  progress: 0,
  uploadID: null,
  setUpload: (uploadID, progress = 0) => set({ uploadID, progress }),
  setProgress: progress => set({ progress }),
  resetUpload: () => set({ uploadID: null, progress: 0 }),
}))
