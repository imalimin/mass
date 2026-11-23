package com.afinn.mass.ui.theme

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

private val MassDarkColorScheme = darkColorScheme(
    primary = MassColors.Accent,
    onPrimary = Color.White,
    primaryContainer = MassColors.AccentSoft,
    onPrimaryContainer = MassColors.AccentText,
    secondary = MassColors.Focus,
    onSecondary = MassColors.Bg,
    secondaryContainer = MassColors.SurfaceRaised,
    onSecondaryContainer = MassColors.Text,
    tertiary = MassColors.AccentText,
    onTertiary = MassColors.Bg,
    tertiaryContainer = MassColors.AccentSoft,
    onTertiaryContainer = MassColors.AccentText,
    background = MassColors.Bg,
    onBackground = MassColors.Text,
    surface = MassColors.Bg,
    onSurface = MassColors.Text,
    surfaceVariant = MassColors.Surface,
    onSurfaceVariant = MassColors.Muted,
    surfaceContainerLowest = MassColors.Bg,
    surfaceContainerLow = MassColors.Surface,
    surfaceContainer = MassColors.Surface,
    surfaceContainerHigh = MassColors.SurfaceRaised,
    surfaceContainerHighest = MassColors.Hover,
    outline = MassColors.Border,
    outlineVariant = MassColors.Border,
    error = MassColors.Danger,
    onError = MassColors.Bg,
    errorContainer = MassColors.ErrorBg,
    onErrorContainer = MassColors.Danger
)

@Composable
fun MassTheme(
    content: @Composable () -> Unit
) {
    MaterialTheme(
        colorScheme = MassDarkColorScheme,
        typography = Typography,
        content = content
    )
}
