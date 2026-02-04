import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import CodeMirror, { ReactCodeMirrorRef } from '@uiw/react-codemirror';
import { yaml } from '@codemirror/lang-yaml';
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search';
import { keymap } from '@codemirror/view';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { IconChevronDown, IconChevronUp, IconSearch } from '@/components/ui/icons';
import { useNotificationStore, useAuthStore, useThemeStore } from '@/stores';
import { configFileApi } from '@/services/api/configFile';
import styles from './ConfigPage.module.scss';

export function ConfigPage() {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);

  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [dirty, setDirty] = useState(false);

  // Search state
  const [searchQuery, setSearchQuery] = useState('');
  const [searchResults, setSearchResults] = useState<{ current: number; total: number }>({ current: 0, total: 0 });
  const [lastSearchedQuery, setLastSearchedQuery] = useState('');
  const editorRef = useRef<ReactCodeMirrorRef>(null);
  const floatingControlsRef = useRef<HTMLDivElement>(null);
  const editorWrapperRef = useRef<HTMLDivElement>(null);

  const disableControls = connectionStatus !== 'connected';

  const loadConfig = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await configFileApi.fetchConfigYaml();
      setContent(data);
      setDirty(false);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(message);
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    loadConfig();
  }, [loadConfig]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await configFileApi.saveConfigYaml(content);
      setDirty(false);
      showNotification(t('config_management.save_success'), 'success');
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      showNotification(`${t('notification.save_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  };

  const handleChange = useCallback((value: string) => {
    setContent(value);
    setDirty(true);
  }, []);

  // Search functionality
  const performSearch = useCallback((query: string, direction: 'next' | 'prev' = 'next') => {
    if (!query || !editorRef.current?.view) return;

    const view = editorRef.current.view;
    const doc = view.state.doc.toString();
    const matches: number[] = [];
    const lowerQuery = query.toLowerCase();
    const lowerDoc = doc.toLowerCase();

    let pos = 0;
    while (pos < lowerDoc.length) {
      const index = lowerDoc.indexOf(lowerQuery, pos);
      if (index === -1) break;
      matches.push(index);
      pos = index + 1;
    }

    if (matches.length === 0) {
      setSearchResults({ current: 0, total: 0 });
      return;
    }

    // Find current match based on cursor position
    const selection = view.state.selection.main;
    const cursorPos = direction === 'prev' ? selection.from : selection.to;
    let currentIndex = 0;

    if (direction === 'next') {
      // Find next match after cursor
      for (let i = 0; i < matches.length; i++) {
        if (matches[i] > cursorPos) {
          currentIndex = i;
          break;
        }
        // If no match after cursor, wrap to first
        if (i === matches.length - 1) {
          currentIndex = 0;
        }
      }
    } else {
      // Find previous match before cursor
      for (let i = matches.length - 1; i >= 0; i--) {
        if (matches[i] < cursorPos) {
          currentIndex = i;
          break;
        }
        // If no match before cursor, wrap to last
        if (i === 0) {
          currentIndex = matches.length - 1;
        }
      }
    }

    const matchPos = matches[currentIndex];
    setSearchResults({ current: currentIndex + 1, total: matches.length });

    // Scroll to and select the match
    view.dispatch({
      selection: { anchor: matchPos, head: matchPos + query.length },
      scrollIntoView: true
    });
    view.focus();
  }, []);

  const handleSearchChange = useCallback((value: string) => {
    setSearchQuery(value);
    // Do not auto-search on each keystroke. Clear previous results when query changes.
    if (!value) {
      setSearchResults({ current: 0, total: 0 });
      setLastSearchedQuery('');
    } else {
      setSearchResults({ current: 0, total: 0 });
    }
  }, []);

  const executeSearch = useCallback((direction: 'next' | 'prev' = 'next') => {
    if (!searchQuery) return;
    setLastSearchedQuery(searchQuery);
    performSearch(searchQuery, direction);
  }, [searchQuery, performSearch]);

  const handleSearchKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      executeSearch(e.shiftKey ? 'prev' : 'next');
    }
  }, [executeSearch]);

  const handlePrevMatch = useCallback(() => {
    if (!lastSearchedQuery) return;
    performSearch(lastSearchedQuery, 'prev');
  }, [lastSearchedQuery, performSearch]);

  const handleNextMatch = useCallback(() => {
    if (!lastSearchedQuery) return;
    performSearch(lastSearchedQuery, 'next');
  }, [lastSearchedQuery, performSearch]);

  // Keep floating controls from covering editor content by syncing its height to a CSS variable.
  useLayoutEffect(() => {
    const controlsEl = floatingControlsRef.current;
    const wrapperEl = editorWrapperRef.current;
    if (!controlsEl || !wrapperEl) return;

    const updatePadding = () => {
      const height = controlsEl.getBoundingClientRect().height;
      wrapperEl.style.setProperty('--floating-controls-height', `${height}px`);
    };

    updatePadding();
    window.addEventListener('resize', updatePadding);

    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(updatePadding);
    ro?.observe(controlsEl);

    return () => {
      ro?.disconnect();
      window.removeEventListener('resize', updatePadding);
    };
  }, []);

  // CodeMirror extensions
  const extensions = useMemo(() => [
    yaml(),
    search(),
    highlightSelectionMatches(),
    keymap.of(searchKeymap)
  ], []);

  // Status text
  const getStatusText = () => {
    if (disableControls) return t('config_management.status_disconnected');
    if (loading) return t('config_management.status_loading');
    if (error) return t('config_management.status_load_failed');
    if (saving) return t('config_management.status_saving');
    if (dirty) return t('config_management.status_dirty');
    return t('config_management.status_loaded');
  };

  const getStatusClass = () => {
    if (error) return styles.error;
    if (dirty) return styles.modified;
    if (!loading && !saving) return styles.saved;
    return '';
  };

  return (
    <div className={styles.container}>
      <h1 className={styles.pageTitle}>{t('config_management.title')}</h1>
      <p className={styles.description}>{t('config_management.description')}</p>

      <Card className={styles.configCard}>
        <div className={styles.content}>
          {/* Editor */}
          {error && <div className="error-box">{error}</div>}
          <div className={styles.editorWrapper} ref={editorWrapperRef}>
            {/* Floating search controls */}
            <div className={styles.floatingControls} ref={floatingControlsRef}>
              <div className={styles.searchInputWrapper}>
                <Input
                  value={searchQuery}
                  onChange={(e) => handleSearchChange(e.target.value)}
                  onKeyDown={handleSearchKeyDown}
                  placeholder={t('config_management.search_placeholder', {
                    defaultValue: '搜索配置内容...'
                  })}
                  disabled={disableControls || loading}
                  className={styles.searchInput}
                  rightElement={
                    <div className={styles.searchRight}>
                      {searchQuery && lastSearchedQuery === searchQuery && (
                        <span className={styles.searchCount}>
                          {searchResults.total > 0
                            ? `${searchResults.current} / ${searchResults.total}`
                            : t('config_management.search_no_results', { defaultValue: '无结果' })}
                        </span>
                      )}
                      <button
                        type="button"
                        className={styles.searchButton}
                        onClick={() => executeSearch('next')}
                        disabled={!searchQuery || disableControls || loading}
                        title={t('config_management.search_button', { defaultValue: '搜索' })}
                      >
                        <IconSearch size={16} />
                      </button>
                    </div>
                  }
                />
              </div>
              <div className={styles.searchActions}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={handlePrevMatch}
                  disabled={!searchQuery || lastSearchedQuery !== searchQuery || searchResults.total === 0}
                  title={t('config_management.search_prev', { defaultValue: '上一个' })}
                >
                  <IconChevronUp size={16} />
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={handleNextMatch}
                  disabled={!searchQuery || lastSearchedQuery !== searchQuery || searchResults.total === 0}
                  title={t('config_management.search_next', { defaultValue: '下一个' })}
                >
                  <IconChevronDown size={16} />
                </Button>
              </div>
            </div>
            <CodeMirror
              ref={editorRef}
              value={content}
              onChange={handleChange}
              extensions={extensions}
              theme={resolvedTheme}
              editable={!disableControls && !loading}
              placeholder={t('config_management.editor_placeholder')}
              height="100%"
              style={{ height: '100%' }}
              basicSetup={{
                lineNumbers: true,
                highlightActiveLineGutter: true,
                highlightActiveLine: true,
                foldGutter: true,
                dropCursor: true,
                allowMultipleSelections: true,
                indentOnInput: true,
                bracketMatching: true,
                closeBrackets: true,
                autocompletion: false,
                rectangularSelection: true,
                crosshairCursor: false,
                highlightSelectionMatches: true,
                closeBracketsKeymap: true,
                searchKeymap: true,
                foldKeymap: true,
                completionKeymap: false,
                lintKeymap: true
              }}
            />
          </div>

          {/* Controls */}
          <div className={styles.controls}>
            <span className={`${styles.status} ${getStatusClass()}`}>
              {getStatusText()}
            </span>
            <div className={styles.actions}>
              <Button variant="secondary" size="sm" onClick={loadConfig} disabled={loading}>
                {t('config_management.reload')}
              </Button>
              <Button size="sm" onClick={handleSave} loading={saving} disabled={disableControls || loading || !dirty}>
                {t('config_management.save')}
              </Button>
            </div>
          </div>
        </div>
      </Card>
    </div>
  );
}
