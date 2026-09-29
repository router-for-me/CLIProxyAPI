export const AUTH_FILES_CHANGED_EVENT = 'auth-files-changed';

export const notifyAuthFilesChanged = () => {
  window.dispatchEvent(new Event(AUTH_FILES_CHANGED_EVENT));
};
