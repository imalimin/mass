package com.afinn.mass

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update

object SessionListCache {
    private val _state = MutableStateFlow(SessionListUiState())
    val state: StateFlow<SessionListUiState> = _state.asStateFlow()

    fun update(transform: (SessionListUiState) -> SessionListUiState) {
        _state.update(transform)
    }

    fun upsertSession(updated: SessionSummary) {
        if (updated.hide) return
        _state.update { current ->
            val nextList = (listOf(updated) + current.sessions.filterNot { it.id == updated.id })
                .sortedWith(compareByDescending<SessionSummary> { it.createdAt }.thenByDescending { it.id })
            current.copy(sessions = nextList)
        }
    }
}
