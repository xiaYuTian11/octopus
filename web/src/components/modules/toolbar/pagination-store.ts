import { create } from 'zustand';
import type { NavItem } from '@/components/modules/navbar';

/** 特殊值：表示"显示全部" */
export const SHOW_ALL_PAGE_SIZE = 9999;

interface PaginationState {
    pages: Partial<Record<NavItem, number>>;
    pageSizes: Partial<Record<NavItem, number>>;
    totalItems: Partial<Record<NavItem, number>>;
    directions: Partial<Record<NavItem, 1 | -1>>;

    getPage: (page: NavItem) => number;
    getPageSize: (page: NavItem) => number;
    getTotalPages: (page: NavItem) => number;
    getDirection: (page: NavItem) => 1 | -1;
    isShowAll: (page: NavItem) => boolean;

    setPage: (page: NavItem, value: number) => void;
    setPageSize: (page: NavItem, value: number) => void;
    setTotalItems: (page: NavItem, value: number) => void;

    prevPage: (page: NavItem) => void;
    nextPage: (page: NavItem) => void;
}

export const usePaginationStore = create<PaginationState>((set, get) => ({
    pages: {},
    pageSizes: {},
    totalItems: {},
    directions: {},

    getPage: (page) => get().pages[page] || 1,
    getPageSize: (page) => get().pageSizes[page] || SHOW_ALL_PAGE_SIZE,
    getTotalPages: (page) => {
        const pageSize = get().pageSizes[page] || SHOW_ALL_PAGE_SIZE;
        // 如果是"显示全部"模式，总页数为1
        if (pageSize >= SHOW_ALL_PAGE_SIZE) return 1;
        const total = get().totalItems[page] || 0;
        return Math.max(1, Math.ceil(total / pageSize));
    },
    getDirection: (page) => get().directions[page] || 1,
    isShowAll: (page) => {
        const pageSize = get().pageSizes[page] || SHOW_ALL_PAGE_SIZE;
        return pageSize >= SHOW_ALL_PAGE_SIZE;
    },

    setPage: (page, value) => {
        const totalPages = get().getTotalPages(page);
        const next = Math.min(totalPages, Math.max(1, Number.isFinite(value) ? Math.trunc(value) : 1));
        const current = get().pages[page] || 1;
        if (current === next) return;
        const direction: 1 | -1 = next >= current ? 1 : -1;
        set((state) => ({
            pages: { ...state.pages, [page]: next },
            directions: { ...state.directions, [page]: direction },
        }));
    },
    setPageSize: (page, value) => {
        const next = Math.min(500, Math.max(1, Number.isFinite(value) ? Math.trunc(value) : 12));
        const current = get().pageSizes[page] || 12;
        if (current === next) return;
        set((state) => ({ pageSizes: { ...state.pageSizes, [page]: next } }));
        queueMicrotask(() => get().setPage(page, get().getPage(page)));
    },
    setTotalItems: (page, value) => {
        const nextTotal = Math.max(0, Number.isFinite(value) ? Math.trunc(value) : 0);
        set((state) => ({ totalItems: { ...state.totalItems, [page]: nextTotal } }));
        // total changes can change total pages; clamp current page
        queueMicrotask(() => get().setPage(page, get().getPage(page)));
    },

    prevPage: (page) => get().setPage(page, get().getPage(page) - 1),
    nextPage: (page) => get().setPage(page, get().getPage(page) + 1),
}));


