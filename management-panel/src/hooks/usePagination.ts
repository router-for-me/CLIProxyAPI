/**
 * 分页 Hook
 */

import { useState, useMemo } from 'react';
import type { PaginationState } from '@/types';

export function usePagination<T>(
  items: T[],
  initialPageSize: number = 20
): PaginationState & {
  currentItems: T[];
  goToPage: (page: number) => void;
  nextPage: () => void;
  prevPage: () => void;
  setPageSize: (size: number) => void;
} {
  const [currentPage, setCurrentPage] = useState(1);
  const [pageSize, setPageSize] = useState(initialPageSize);

  const totalItems = items.length;
  const totalPages = Math.ceil(totalItems / pageSize) || 1;

  const currentItems = useMemo(() => {
    const start = (currentPage - 1) * pageSize;
    const end = start + pageSize;
    return items.slice(start, end);
  }, [items, currentPage, pageSize]);

  const goToPage = (page: number) => {
    const validPage = Math.max(1, Math.min(page, totalPages));
    setCurrentPage(validPage);
  };

  const nextPage = () => {
    goToPage(currentPage + 1);
  };

  const prevPage = () => {
    goToPage(currentPage - 1);
  };

  const handleSetPageSize = (size: number) => {
    setPageSize(size);
    setCurrentPage(1); // 重置到第一页
  };

  return {
    currentPage,
    pageSize,
    totalPages,
    totalItems,
    currentItems,
    goToPage,
    nextPage,
    prevPage,
    setPageSize: handleSetPageSize
  };
}
